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

func TestDownloadExpiryEndsGETWithoutClientHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		now := time.Now()
		ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: now.Add(-time.Minute), limit: w.v.size, media: estimateMediaTiming(w.v.size, 0)})
		d, release := addTestDemand(t, s, ctx, w.v, true)
		defer release()
		d.flight = w.f
		s.expireDownload(w.f, w.v.key, 0)
		if !errors.Is(context.Cause(d.ctx), blocksched.ErrExpired) || ctx.Err() != nil || useCount(s) != 0 || len(s.useHandoffs) != 0 {
			t.Fatal("service expiry lost its cause, canceled the client context or created a handoff")
		}
		if d.exit.at.Load().reason != withdrawalNeedEnded || s.protection[w.v.key][0].until != now.Add(s.cfg.LocalProtectionGrace) {
			t.Fatal("withdrawal did not establish protection at the real source exit")
		}
		time.Sleep(time.Second)
		s.expireDownload(w.f, w.v.key, 0)
		release()
		if s.protection[w.v.key][0].until != now.Add(s.cfg.LocalProtectionGrace) {
			t.Fatal("replayed failure extended the protection tail")
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
		ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: time.Now().Add(-time.Minute), limit: w.v.size, media: estimateMediaTiming(w.v.size, 0)})
		get, releaseGET := addTestDemand(t, s, ctx, w.v, true)
		defer releaseGET()
		get.flight = w.f
		s.expireDownload(w.f, w.v.key, 0)
		plan, _, _ := w.plan(time.Now(), true)
		if queue.ctx.Err() != nil || plan[0].need.ReadyBy != queue.queueSource.readyBy(0, rangeBlockSize) || !plan[0].need.Opportunistic || len(s.useHandoffs) != 0 {
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
	old := blocksched.Need{Owner: "video", ReadyBy: now.Add(-time.Minute), LeaseUntil: now.Add(handoffGrace)}
	plan := map[blockSource]plannedNeed{{"handoff", 1}: {need: old, source: sourceHandoff}}
	use := aggregateUse(plan, now)
	if use.need.Owner != "" || use.protectReady != old.ReadyBy {
		t.Fatal("expired handoff retained execution or lost usage protection", use)
	}
	plan[blockSource{"batch", 1}] = plannedNeed{need: blocksched.Need{Owner: "video", LeaseUntil: now.Add(sourceLease)}, source: sourceBatch}
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
		reader.Close()
	}
	if !errors.Is(err, blocksched.ErrExpired) || cacheErrorStatus(err) != http.StatusGatewayTimeout || ctx.Err() != nil || calls.Load() != 0 {
		t.Fatal("expired registration did not fail explicitly before HTTP work", err, calls.Load())
	}
	if useCount(s) != 0 || s.StorageError() != nil {
		t.Fatal("failed GET retained demand or triggered the storage fuse")
	}
}

func TestExpiredGETAllowsManualPreparationAfterCleanup(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusPartialContent} {
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
			if calls.Load() != 2 || !errors.Is(old.err, blocksched.ErrExpired) {
				t.Fatal("expired task was reused or retried more than once", calls.Load(), old.err)
			}
			if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(got) != payload {
				t.Fatal("manual retry was not verified and published", err)
			}
			s.mu.Lock()
			handoffs := len(s.useHandoffs)
			s.mu.Unlock()
			if handoffs != 0 || s.StorageError() != nil {
				t.Fatal("service deadline created handoff work or poisoned storage")
			}
		})
	}
}
