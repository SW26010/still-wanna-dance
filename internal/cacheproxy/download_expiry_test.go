package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func (s *Server) expireDownload(f *flight, key string, block int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshDownloadDeadlinesLocked(f, key, time.Now(), func(span useSpan) (int64, bool) { return block, span.contains(key, block) })
}

func snapshotUseHandoffs(s *Server) []*useHandoff {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*useHandoff(nil), s.useHandoffs...)
}

func TestDownloadExpirySelectsGETsByRemainingSpanAndOwnClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		now := time.Now()
		for _, tc := range []struct {
			name    string
			at      time.Time
			offset  int64
			affixed bool
			expired bool
		}{
			{"expired", now.Add(-sourceGrace), 0, true, true},
			{"no-deadline", time.Time{}, 0, true, false},
			{"later-clock", now.Add(time.Second - sourceGrace), 0, true, false},
			{"other-range", now.Add(-sourceGrace), rangeBlockSize, true, false},
			{"other-flight", now.Add(-sourceGrace), 0, false, false},
		} {
			clock := &playbackClock{at: tc.at, offset: tc.offset, limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
			d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
			defer release()
			if tc.affixed {
				d.flight = w.f
			}
			defer func() {
				if got := errors.Is(context.Cause(d.ctx), errSourceExpired); got != tc.expired {
					t.Error("GET expiry ignored its own remaining span or clock", tc.name, context.Cause(d.ctx))
				}
			}()
		}
		s.expireDownload(w.f, w.v.key, 0)
	})
}

func TestGETDeadlineOnFinalRangeFailure(t *testing.T) {
	for _, failure := range []error{errUpstreamDownload, ErrLocalStorage} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := testSession(t)
				w.v.size = rangeBlockSize
				addGET := func(at time.Time) *downloadDemand {
					clock := &playbackClock{at: at, limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
					d, release := addTestDemand(t, s, context.WithValue(t.Context(), playbackClockKey{}, clock), w.v, true)
					t.Cleanup(release)
					d.flight = w.f
					return d
				}
				now := time.Now()
				a, b := addGET(now.Add(time.Second-sourceGrace)), addGET(now)
				plan, _, _ := w.plan(now)
				task, demand, err := s.scheduler.Schedule(w.reads.Key(0), rangeBlockSize, func(context.Context, *blocksched.Progress) blocksched.Result {
					time.Sleep(time.Second)
					return blocksched.Result{Err: failure}
				}, plan[0].need)
				if err != nil {
					t.Fatal(err)
				}
				w.jobs[0] = &scheduledBlock{task: task, merged: demand}
				<-task.Done() // A's deadline and the final range error are both observable.
				_, err = w.nextPlan()
				if got := w.close(err); got != failure || b.ctx.Err() != nil {
					t.Fatal("shared failure or surviving source changed", got, context.Cause(b.ctx))
				}
				wantExpired := failure == errUpstreamDownload
				if errors.Is(context.Cause(a.ctx), errSourceExpired) != wantExpired || (len(snapshotUseHandoffs(s)) == 1) != wantExpired {
					t.Fatal("incorrect GET outcome at final range failure", failure, context.Cause(a.ctx), len(snapshotUseHandoffs(s)))
				}
			})
		})
	}
}

func TestReadableBlockDoesNotExpireGET(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		now := time.Now()
		clock := &playbackClock{at: now.Add(-sourceGrace), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
		d, release := addTestDemand(t, s, context.WithValue(t.Context(), playbackClockKey{}, clock), w.v, true)
		defer release()
		d.flight = w.f
		w.f.ranges.done[0] = true
		w.plan(now)
		if d.ctx.Err() != nil || !w.sourceAt.Equal(sourceDeadline(clock.ready(rangeBlockSize))) {
			t.Fatalf("readable first block affected GET: err=%v next=%v", d.ctx.Err(), w.sourceAt)
		}
		w.f.ranges.done[1] = true
		w.plan(w.sourceAt)
		if d.ctx.Err() != nil || !w.sourceAt.IsZero() {
			t.Fatalf("all blocks readable: err=%v next=%v", d.ctx.Err(), w.sourceAt)
		}
	})
}

func TestGETDeliversBlockStoredBetweenDeadlineAndReevaluation(t *testing.T) {
	s, _ := setup(t, nil)
	ready, closed := make(chan int64, 1), make(chan int64, 1)
	gate := make(chan struct{})
	releaseBody := sync.OnceFunc(func() { close(gate) })
	defer releaseBody()
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(req *http.Request) (*http.Response, error) {
			resp := fixtureRangeResponse(req, payload)
			resp.Body = &continuingRangeBody{ctx: req.Context(), remainder: resp.Body, ready: ready, closed: closed, gate: gate}
			return resp, nil
		})}
	}
	v := parsedVideo(t, s, payload)
	deadline := time.Now().Add(2 * time.Second)
	clock := &playbackClock{at: deadline.Add(-sourceGrace), limit: v.size, media: estimateMediaTiming(v.size, 0)}
	f, reader, err := s.obtain(context.WithValue(t.Context(), playbackClockKey{}, clock), v)
	if err != nil || reader == nil {
		t.Fatal("GET did not open its stream", err)
	}
	defer reader.Close()
	receiveDemandEvent(t, ready)

	// Hold off source reevaluation until the complete block is visible. The
	// body stays gated through the deadline, so it cannot have arrived early.
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !time.Now().Before(deadline) {
			t.Fatal("source deadline already passed before delaying reevaluation")
		}
		time.Sleep(time.Until(deadline))
		if f.ranges.has(0) {
			t.Fatal("block was already readable at the deadline")
		}
		f.spool.mu.Lock()
		changed := f.spool.changed
		f.spool.mu.Unlock()
		releaseBody()
		receiveDemandEvent(t, changed)
		if !f.ranges.has(0) {
			t.Fatal("block did not become readable before reevaluation")
		}
	}()
	receiveDemandEvent(t, f.done)
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != payload || len(snapshotUseHandoffs(s)) != 0 {
		t.Fatalf("GET should deliver the complete block without handoff: body=%q err=%v", got, err)
	}
}

func TestAddressRetryDeadlineFollowsFirstMissingBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		clock := &playbackClock{at: time.Now().Add(time.Second - sourceGrace), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
		a, release := addTestDemand(t, s, context.WithValue(t.Context(), playbackClockKey{}, clock), w.v, true)
		defer release()
		a.flight = &flight{err: errUpstreamDownload, done: make(chan struct{})}
		close(a.flight.done)
		finishWait := s.watchRetryDeadline(a)
		defer finishWait()
		// A later flight makes only the first block locally readable.
		s.retentionMu.Lock()
		s.localResources = map[string]*localResource{w.v.key: w.f.ranges}
		s.retentionMu.Unlock()
		w.f.ranges.finish(0)
		time.Sleep(time.Until(sourceDeadline(clock.ready(0))))
		synctest.Wait()
		if a.ctx.Err() != nil {
			t.Fatal("readable first block expired the GET", context.Cause(a.ctx))
		}
		time.Sleep(time.Until(sourceDeadline(clock.ready(rangeBlockSize))))
		synctest.Wait()
		if !errors.Is(context.Cause(a.ctx), errSourceExpired) {
			t.Fatal("waiting GET missed the next missing block's deadline", context.Cause(a.ctx))
		}
	})
}

func TestExpiredGETAcrossFlightAttachment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		now := time.Now()
		clock := &playbackClock{at: now.Add(-sourceGrace), limit: w.v.size, media: mediaTiming{w.v.size, 2 * time.Minute}}
		a, releaseA := addTestDemand(t, s, context.WithValue(t.Context(), playbackClockKey{}, clock), w.v, true)
		defer releaseA()
		plan, _, _ := w.plan(now)
		if plan[0].need.Owner != "" || plan[1].need.Owner != "" || plan[0].protectReady != clock.at || a.ctx.Err() != nil {
			t.Fatal("unattached GET funded a download or lost its usage", plan[0])
		}

		// A is waiting for a replacement address for the failed flight while
		// B attaches to a new flight for the same missing data.
		a.flight = &flight{err: errUpstreamDownload, done: make(chan struct{})}
		close(a.flight.done)
		bClock := &playbackClock{at: now, limit: rangeBlockSize, media: clock.media}
		b, releaseB := addTestDemand(t, s, context.WithValue(t.Context(), playbackClockKey{}, bClock), w.v, true)
		defer releaseB()
		b.flight = w.f
		plan, _, _ = w.plan(now)
		if !errors.Is(context.Cause(a.ctx), errSourceExpired) || b.ctx.Err() != nil || plan[0].need.ReadyBy != now {
			t.Fatal("failed GET changed the new flight's clock or lost its retry cause", plan[0], context.Cause(a.ctx))
		}
	})
}

func TestQueuedRangeRechecksSourceBeforeFirstRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		s.cfg.DownloadTimeout = time.Minute
		w.f.ranges.video = w.v
		started := 0
		s.cfg.ResourceTransports = func(string) []http.RoundTripper {
			started++
			return []http.RoundTripper{resourceTransportFunc(func(*http.Request) (*http.Response, error) {
				return nil, io.ErrUnexpectedEOF
			})}
		}
		now := time.Now()
		held, stop, err := s.scheduler.Schedule("occupied", 1, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
			<-ctx.Done()
			return blocksched.Result{}
		}, blocksched.Need{Owner: "other", LeaseUntil: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		clock := &playbackClock{at: now.Add(time.Second - sourceGrace), limit: w.v.size, media: mediaTiming{w.v.size, 2 * time.Minute}}
		a, release := addTestDemand(t, s, context.WithValue(t.Context(), playbackClockKey{}, clock), w.v, true)
		defer release()
		a.flight = w.f
		reconcileSession(t, w)
		time.Sleep(time.Second)
		stop.Close() // Capacity returns before the source coordinator replans.
		<-held.Done()
		for block, job := range w.jobs {
			if err := job.task.Err(); !errors.Is(err, blocksched.ErrDeferred) {
				t.Fatal("stale admission reached the upstream", block, err)
			}
		}
		if started != 0 {
			t.Fatal("sent requests after the GET missed its first block", started)
		}
		if _, err := w.nextPlan(); err != nil || !errors.Is(context.Cause(a.ctx), errSourceExpired) {
			t.Fatal("deferred admission failed the flight or lost its source outcome", err, context.Cause(a.ctx))
		}
	})
}

func TestExpiredGETHandoffUsesEntireUnconsumedRangeAndFixedExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 8 * rangeBlockSize
		now := time.Now()
		clock := &playbackClock{at: now.Add(-40 * time.Second), limit: w.v.size, media: mediaTiming{w.v.size, 80 * time.Second}}
		d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
		defer release()
		d.flight = w.f
		g := &getConsumption{s: s, demand: d, size: w.v.size}
		g.start(http.StatusOK, nil)
		g.accept(rangeBlockSize + rangeBlockSize/2) // Block 0 delivered; 1 remains partially delivered.
		w.f.ranges.done[6] = true                   // Readability does not change usage eligibility.
		started := make(chan struct{})
		// Block 3 is already past need-by, but its download deadline is still valid.
		n := blocksched.Need{Owner: w.v.key, ReadyBy: clock.ready(3 * rangeBlockSize), LeaseUntil: now.Add(time.Hour)}
		task, demand, err := s.scheduler.Schedule(w.reads.Key(3), rangeBlockSize, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
			close(started)
			<-ctx.Done()
			return blocksched.Result{Err: ctx.Err()}
		}, n)
		if err != nil {
			t.Fatal(err)
		}
		<-started
		w.jobs[3] = &scheduledBlock{task: task, merged: demand, published: n}
		s.expireDownload(w.f, w.v.key, 1)
		handoffs := snapshotUseHandoffs(s)
		if !errors.Is(context.Cause(d.ctx), errSourceExpired) || len(s.demands) != 0 || len(handoffs) != 1 || useCount(s) != 7 {
			t.Fatal("sole expired GET failed to transfer its eligible usage", context.Cause(d.ctx), len(handoffs), useCount(s))
		}
		h := handoffs[0]
		if h.span != (useSpan{w.v.key, 1, 8}) || h.clock != *clock || h.until != now.Add(2*time.Second) {
			t.Fatal("handoff lost remaining usage, changed N or refreshed its fixed expiry", h)
		}
		plan, _, _ := w.plan(now)
		if plan[1].need.Owner != "" || plan[6].need.Owner != "" {
			t.Fatal("handoff scheduled expired or readable data", plan)
		}
		for _, block := range []int64{2, 3, 4, 5, 7} {
			if !plan[block].need.WindowClosed || plan[block].need.ReadyBy != clock.ready(block*rangeBlockSize) {
				t.Fatal("handoff changed N or gained startup permission", block, plan[block])
			}
		}
		reconcileSession(t, w)
		time.Sleep(2*time.Second - time.Nanosecond)
		s.expireDownload(w.f, w.v.key, 1)
		release()
		handoffs = snapshotUseHandoffs(s)
		if len(handoffs) != 1 || handoffs[0] != h {
			t.Fatal("replayed source exit replaced or extended handoff")
		}
		select {
		case <-task.Done():
			t.Fatal("GET expiry canceled a still-valid execution", task.Err())
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if useCount(s) != 0 || len(snapshotUseHandoffs(s)) != 0 {
			t.Fatal("handoff outlived E+2 seconds")
		}
		reconcileSession(t, w)
		if err := task.Err(); !errors.Is(err, context.Canceled) {
			t.Fatal("E+2 did not stop unsupported execution", err)
		}
	})
}

func TestDownloadExpiryWithoutFutureUsageKeepsOnlyHandoffProtection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		now := time.Now()
		ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: now.Add(-time.Minute), limit: w.v.size, media: mediaTiming{w.v.size, time.Second}})
		d, release := addTestDemand(t, s, ctx, w.v, true)
		defer release()
		d.flight = w.f
		s.expireDownload(w.f, w.v.key, 0)
		if !errors.Is(context.Cause(d.ctx), errSourceExpired) || ctx.Err() != nil || useCount(s) != 20 || len(s.useHandoffs) != 1 {
			t.Fatal("service expiry lost its cause, canceled the client context or lost remaining usage")
		}
		plan, _, _ := w.plan(now)
		for block, use := range plan {
			if use.need.Owner != "" || use.protectReady.IsZero() {
				t.Fatal("expired handoff gained download qualification or lost protection", block, use)
			}
		}
		if d.exit.at.Load().reason != withdrawalDownloadExpired || !s.protection[w.v.key][0].until.IsZero() {
			t.Fatal("handoff started its protection tail before usage ended")
		}
		time.Sleep(time.Second)
		s.expireDownload(w.f, w.v.key, 0)
		release()
		time.Sleep(time.Second)
		synctest.Wait()
		if useCount(s) != 0 || len(s.useHandoffs) != 0 || s.protection[w.v.key][0].until != now.Add(handoffGrace+s.cfg.LocalProtectionGrace) {
			t.Fatal("replayed failure extended the protection tail")
		}
	})
}

func TestExpiredGETHandoffDoesNotExtendRunningDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		now := time.Now()
		clock := &playbackClock{at: now.Add(-sourceGrace), limit: w.v.size, media: mediaTiming{w.v.size, 2 * time.Second}}
		d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
		defer release()
		d.flight = w.f
		started := make(chan struct{})
		n := blocksched.Need{Owner: w.v.key, ReadyBy: clock.ready(rangeBlockSize), LeaseUntil: now.Add(time.Hour)}
		task, demand, err := s.scheduler.Schedule(w.reads.Key(1), rangeBlockSize, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
			close(started)
			<-ctx.Done()
			return blocksched.Result{Err: context.Cause(ctx)}
		}, n)
		if err != nil {
			t.Fatal(err)
		}
		<-started
		w.jobs[1] = &scheduledBlock{task: task, merged: demand, published: n}
		s.expireDownload(w.f, w.v.key, 0)
		reconcileSession(t, w)
		time.Sleep(time.Second - time.Nanosecond)
		select {
		case <-task.Done():
			t.Fatal("handoff stopped valid work before its deadline", task.Err())
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		reconcileSession(t, w)
		if err := task.Err(); !errors.Is(err, context.Canceled) {
			t.Fatal("handoff extended the original download deadline", err)
		}
		if len(snapshotUseHandoffs(s)) != 1 || useCount(s) != 2 {
			t.Fatal("download expiry ended usage before the fixed handoff expiry")
		}
	})
}

func TestDelayedExpiredGETHandoffKeepsFrozenExitAndIndependentGET(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		exit := time.Now()
		clock := &playbackClock{at: exit.Add(-sourceGrace), limit: w.v.size, media: mediaTiming{w.v.size, 61 * time.Second}}
		d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
		defer release()
		// Delayed collection must preserve the whole remaining span and the
		// original exit time, even when a block's need-by passes in the meantime.
		d.exit.recordReason(withdrawalDownloadExpired)
		d.cancel(errSourceExpired)
		time.Sleep(time.Second)
		w.plan(time.Now())
		handoffs := snapshotUseHandoffs(s)
		if len(handoffs) != 1 || handoffs[0].span != (useSpan{w.v.key, 0, 2}) || handoffs[0].until != exit.Add(2*time.Second) {
			t.Fatal("delayed exit changed remaining usage or fixed expiry")
		}
		h := handoffs[0]
		nextClock := &playbackClock{at: time.Now(), offset: rangeBlockSize, limit: w.v.size, media: clock.media}
		ctx := context.WithValue(context.WithValue(context.Background(), httpGETKey{}, true), playbackClockKey{}, nextClock)
		next, end := addTestDemand(t, s, ctx, w.v, true)
		defer end()
		plan, _, _ := w.plan(time.Now())
		if plan[1].need.WindowClosed || plan[1].need.ReadyBy != nextClock.at || h.until != exit.Add(2*time.Second) {
			t.Fatal("handoff blocked a new source or renewed itself")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		plan, _, _ = w.plan(time.Now())
		if len(snapshotUseHandoffs(s)) != 0 || useCount(s) != 1 || next.ctx.Err() != nil || plan[1].need.WindowClosed || plan[1].need.ReadyBy != nextClock.at {
			t.Fatal("handoff expiry withdrew the independent source", plan[1])
		}
	})
}

func TestDownloadExpiryRetainsManualAndLaterQueueSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		_, releaseManual := addTestDemand(t, s, context.Background(), w.v, false)
		defer releaseManual()
		s.UpdatePrefetchQueue([]int64{1}, false)
		v := w.v
		v.songID = "1"
		queue, releaseQueue := addTestDemand(t, s, s.queueDemandContext(context.Background(), "1"), v, false)
		defer releaseQueue()
		ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: time.Now().Add(-time.Minute), limit: w.v.size, media: mediaTiming{w.v.size, time.Second}})
		get, releaseGET := addTestDemand(t, s, ctx, w.v, true)
		defer releaseGET()
		get.flight = w.f
		s.expireDownload(w.f, w.v.key, 0)
		plan, _, _ := w.plan(time.Now())
		if queue.ctx.Err() != nil || plan[0].need.ReadyBy != queue.queueSource.readyBy(0, rangeBlockSize) || !plan[0].need.Opportunistic || len(s.useHandoffs) != 1 {
			t.Fatal("old GET failure erased or bypassed remaining source timing", plan[0])
		}
	})
}

func TestDownloadExpiryRetimesQueueAfterAllSharedGETsEnd(t *testing.T) {
	// Exercise registry iteration orders; all failed sources belong to the
	// same event, so none may become the queue's temporary surviving anchor.
	for range 32 {
		w, s := testSession(t)
		s.UpdatePrefetchQueue([]int64{1}, false)
		now := time.Now()
		for _, duration := range []time.Duration{2 * time.Minute, 3 * time.Minute} {
			ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: now.Add(-time.Minute), limit: w.v.size, media: mediaTiming{w.v.size, duration}})
			d, release := addTestDemand(t, s, ctx, w.v, true)
			t.Cleanup(release)
			d.flight = w.f
		}
		s.expireDownload(w.f, w.v.key, 0)
		if got := s.prefetchSources["1"].startAt; got != now.Add(time.Minute) {
			t.Fatal("batch expiry retained a temporary queue anchor", got, now.Add(time.Minute))
		}
	}
}

func TestExpiredQueuePreparationRequiresActualSourceChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		releaseClock := holdQueuePlaybackUntil(t, s, time.Now().Add(-time.Minute))
		s.UpdatePrefetchQueue([]int64{1}, false)
		v := w.v
		v.songID = "1"
		ctx := s.queueDemandContext(context.Background(), "1")
		d, release := addTestDemand(t, s, ctx, v, false)
		defer release()
		s.expireDownload(w.f, v.key, 0)
		if !errors.Is(context.Cause(d.ctx), errSourceExpired) || useCount(s) != 1 || !d.queueSource.expired {
			t.Fatal("expired predictive preparation retained usage")
		}
		s.UpdatePrefetchQueue([]int64{1}, false)
		if _, _, err := s.addDemand(ctx, v, false); !errors.Is(err, errSourceExpired) {
			t.Fatal("repeated snapshot/retry revived an expired queue preparation", err)
		}
		// A genuine new playback changes the queue's remaining clock. Its next
		// preparation is allowed, still subject to the normal download window.
		releaseClock()
		_, releaseGET := addTestDemand(t, s, context.Background(), video{key: "playing", size: 1}, true)
		defer releaseGET()
		_, releaseNext := addTestDemand(t, s, ctx, v, false)
		defer releaseNext()
		if d.queueSource.expired || !d.queueSource.readyBy(0, rangeBlockSize).After(time.Now()) {
			t.Fatal("real source change could not rearm preparation")
		}
	})
}

func TestExpiredHandoffKeepsUsageWithoutDownloadQualification(t *testing.T) {
	now := time.Now()
	old := blocksched.Need{Owner: "video", ReadyBy: now.Add(-time.Minute), LeaseUntil: now.Add(handoffGrace), WindowClosed: true}
	plan := map[blockSource]plannedNeed{{"handoff", 1}: {need: old}}
	use := aggregateUse(plan, now)
	if use.need.Owner != "" || use.protectReady != old.ReadyBy {
		t.Fatal("expired handoff retained execution or lost usage protection", use)
	}
	plan[blockSource{"batch", 1}] = plannedNeed{need: blocksched.Need{Owner: "video", LeaseUntil: now.Add(sourceLease)}}
	use = aggregateUse(plan, now)
	if !use.need.ReadyBy.IsZero() || !use.need.Opportunistic || use.protectReady != old.ReadyBy {
		t.Fatal("expired handoff poisoned independent untimed qualification", use)
	}
}

func TestExpiredGETBeforeHeadersFailsWithoutUpstream(t *testing.T) {
	s, _ := setup(t, nil)
	var calls atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		calls.Add(1)
		return nil
	}
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parseResolved(r)
	ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: time.Now().Add(-time.Minute), limit: v.size, media: estimateMediaTiming(v.size, 0)})
	_, reader, err := s.obtain(ctx, v)
	if reader != nil {
		_, err = io.ReadAll(reader)
		reader.Close()
	}
	if !errors.Is(err, errSourceExpired) || cacheErrorStatus(err) != http.StatusGatewayTimeout || ctx.Err() != nil || calls.Load() != 0 {
		t.Fatal("expired registration did not fail explicitly before HTTP work", err, calls.Load())
	}
	s.mu.Lock()
	handoffs := len(s.useHandoffs)
	activeGETs := len(s.demands)
	s.mu.Unlock()
	if handoffs != 1 || activeGETs != 0 || s.StorageError() != nil {
		t.Fatal("failed GET lost its handoff, retained its source or triggered the storage fuse")
	}
}

func TestExpiredGETPreservesSharedPartialRange(t *testing.T) {
	s, _ := setup(t, nil)
	var calls atomic.Int32
	prefix, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(req *http.Request) (*http.Response, error) {
			response := fixtureRangeResponse(req, payload)
			if calls.Add(1) != 1 {
				return response, nil
			}
			body := strings.NewReader(payload)
			response.Body = io.NopCloser(publishReaderFunc(func(data []byte) (int, error) {
				if body.Len() == len(payload) {
					n, err := body.Read(data[:1])
					return n, err
				}
				if body.Len() == len(payload)-1 {
					close(prefix)
				}
				select {
				case <-req.Context().Done():
					return 0, req.Context().Err()
				case <-release:
					return body.Read(data)
				}
			}))
			return response, nil
		})}
	}
	req, _ := http.NewRequest("GET", videoURL(payload), nil)
	video, _ := s.parseResolved(req)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	clock := &playbackClock{at: time.Now(), limit: video.size, media: estimateMediaTiming(video.size, 0)}
	_, first, err := s.obtain(context.WithValue(ctx, playbackClockKey{}, clock), video)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	result := make(chan error, 1)
	go func() { _, err := io.ReadAll(first); result <- err }()
	receiveDemandEvent(t, prefix)
	_, second, err := s.obtain(ctx, video)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	s.mu.Lock()
	clock.at = time.Now().Add(-sourceGrace)
	s.notifyUseChangedLocked(video.key)
	s.mu.Unlock()
	if err := receiveDemandEvent(t, result); !errors.Is(err, errSourceExpired) {
		t.Fatalf("first GET: %v", err)
	}
	finish()
	got, err := io.ReadAll(second)
	if err != nil || string(got) != payload || calls.Load() != 1 {
		t.Fatalf("second GET: body=%q calls=%d err=%v", got, calls.Load(), err)
	}
}
