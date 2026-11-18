package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

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
			{"expired", now.Add(-blocksched.DownloadGrace), 0, true, true},
			{"later-clock", now.Add(time.Second - blocksched.DownloadGrace), 0, true, false},
			{"other-range", now.Add(-blocksched.DownloadGrace), rangeBlockSize, true, false},
			{"other-flight", now.Add(-blocksched.DownloadGrace), 0, false, false},
		} {
			clock := &playbackClock{at: tc.at, offset: tc.offset, limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
			d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
			defer release()
			if tc.affixed {
				d.flight = w.f
			}
			defer func() {
				if got := errors.Is(context.Cause(d.ctx), blocksched.ErrExpired); got != tc.expired {
					t.Error("GET expiry ignored its own remaining span or clock", tc.name, context.Cause(d.ctx))
				}
			}()
		}
		s.expireDownload(w.f, w.v.key, 0)
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
		if !errors.Is(context.Cause(d.ctx), blocksched.ErrExpired) || len(s.demands) != 0 || len(handoffs) != 1 || useCount(s) != 7 {
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
		if !errors.Is(context.Cause(d.ctx), blocksched.ErrExpired) || ctx.Err() != nil || useCount(s) != 20 || len(s.useHandoffs) != 1 {
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
		clock := &playbackClock{at: now.Add(-blocksched.DownloadGrace), limit: w.v.size, media: mediaTiming{w.v.size, 2 * time.Second}}
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
		if err := task.Err(); !errors.Is(err, blocksched.ErrExpired) {
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
		clock := &playbackClock{at: exit.Add(-blocksched.DownloadGrace), limit: w.v.size, media: mediaTiming{w.v.size, 61 * time.Second}}
		d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
		defer release()
		// Delayed collection must preserve the whole remaining span and the
		// original exit time, even when a block's need-by passes in the meantime.
		d.exit.recordReason(withdrawalDownloadExpired)
		d.cancel(blocksched.ErrExpired)
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
		if !errors.Is(context.Cause(d.ctx), blocksched.ErrExpired) || useCount(s) != 1 || !d.queueSource.expired {
			t.Fatal("expired predictive preparation retained usage")
		}
		s.UpdatePrefetchQueue([]int64{1}, false)
		if _, _, err := s.addDemand(ctx, v, false); !errors.Is(err, blocksched.ErrExpired) {
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
	if !errors.Is(err, blocksched.ErrExpired) || cacheErrorStatus(err) != http.StatusGatewayTimeout || ctx.Err() != nil || calls.Load() != 0 {
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

func TestExpiredGETAllowsManualPreparationAfterCleanup(t *testing.T) {
	for _, status := range []int{http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, cfg := setup(t, nil)
			cleanup, gate := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(gate) })
			defer unblock()
			var loads, calls atomic.Int32
			s.cfg.BeginResourceLoad = func() func() {
				if loads.Add(1) == 1 {
					return func() { close(cleanup); <-gate }
				}
				return func() {}
			}
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
					if calls.Add(1) == 1 {
						body = io.NopCloser(publishReaderFunc(func([]byte) (int, error) {
							<-r.Context().Done()
							<-gate
							return 0, r.Context().Err()
						}))
					}
					header := make(http.Header)
					if status == http.StatusPartialContent {
						header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
					}
					return &http.Response{StatusCode: status, ContentLength: int64(len(payload)), Header: header, Body: body, Request: r}, nil
				})}
			}
			r, _ := http.NewRequest("GET", videoURL(payload), nil)
			v, _ := s.parseResolved(r)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			getCtx := context.WithValue(ctx, playbackClockKey{}, &playbackClock{at: time.Now().Add(time.Second - blocksched.DownloadGrace), limit: v.size, media: estimateMediaTiming(v.size, 0)})
			old, reader, err := s.obtain(getCtx, v)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			getResult := make(chan error, 1)
			go func() { _, err := io.ReadAll(reader); getResult <- err }()
			manualCtx, admitted := WithPreparationAdmission(ctx)
			manualResult := make(chan error, 1)
			go func() { _, err := s.prefetchVideo(manualCtx, v); manualResult <- err }()
			receiveDemandEvent(t, admitted)
			receiveDemandEvent(t, cleanup)
			if calls.Load() != 1 {
				t.Fatal("new execution overlapped expired response cleanup", calls.Load())
			}
			unblock()
			if err := receiveDemandEvent(t, getResult); !errors.Is(err, blocksched.ErrExpired) || getCtx.Err() != nil {
				t.Fatal("GET did not receive an explicit service deadline failure", err)
			}
			if err := receiveDemandEvent(t, manualResult); err != nil {
				t.Fatal("independent manual demand could not finish after cleanup", err)
			}
			if calls.Load() != 2 || old.err != nil {
				t.Fatal("expired task was reused or retried more than once", calls.Load(), old.err)
			}
			if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(got) != payload {
				t.Fatal("manual retry was not verified and published", err)
			}
			if s.StorageError() != nil {
				t.Fatal("service deadline poisoned storage")
			}
		})
	}
}
