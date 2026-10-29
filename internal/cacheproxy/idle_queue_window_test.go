package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Reproduce the review's 125 s block / 120 s window with real local storage,
// controlled HTTP ranges and zero retention budget, without a listening socket.
func idleBoundaryServer(t *testing.T, seconds int, protect time.Duration) (*Server, *[2]atomic.Int32, string) {
	t.Helper()
	body := strings.Repeat("r", int(2*rangeBlockSize))
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	cfg.CacheUnlimited, cfg.VideoBudgetBytes = false, 0
	cfg.PrepareAhead, cfg.LocalProtectAhead = 120*time.Second, protect
	cfg.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(body), nil }
	calls := new([2]atomic.Int32)
	base := time.Now()
	cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
			block := start / rangeBlockSize
			n := calls[block].Add(1)
			t.Logf("t=%s block=%d request=%d", time.Since(base), block, n)
			return &http.Response{StatusCode: http.StatusPartialContent, Request: r,
				ContentLength: end - start + 1, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
				Body: io.NopCloser(strings.NewReader(body[start : end+1]))}, nil
		})}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	key := videoKey(body)
	if _, err := s.usage.db.Exec(`INSERT INTO songs(id,end) VALUES (42,?)`, seconds); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO media(md5,byte_size) VALUES (?,?)`, key, len(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO song_media(song_id,md5) VALUES (42,?)`, key); err != nil {
		t.Fatal(err)
	}
	return s, calls, key
}

func TestIdleQueueBoundaryDoesNotEnterBetweenRefreshes(t *testing.T) {
	for _, seconds := range []int{240, 250} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, calls, key := idleBoundaryServer(t, seconds, 120*time.Second)
				if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { _, err := s.PrefetchQueuePlayback(t.Context(), 42); done <- err }()
				for _, delay := range []time.Duration{5, 5, 10, 10, 45} {
					time.Sleep(delay * time.Second)
					synctest.Wait()
					want := int32(0)
					if seconds == 240 {
						want = 1 // Equality belongs to both windows.
					}
					if calls[0].Load() != 1 || calls[1].Load() != want {
						t.Fatalf("idle queue crossed/refilled boundary: calls=%d,%d", calls[0].Load(), calls[1].Load())
					}
					if protectionFor(s, key, 1).active(time.Now()) != (seconds == 240) {
						t.Fatal("protection crossed the relative boundary")
					}
				}
				if seconds == 250 {
					select {
					case err := <-done:
						t.Fatal("preparation ended while a block remained outside the window", err)
					default:
					}
					// A real metadata change moves the same source into both windows.
					if _, err := s.usage.db.Exec(`UPDATE songs SET end=240 WHERE id=42`); err != nil {
						t.Fatal(err)
					}
					if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
						t.Fatal(err)
					}
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				time.Sleep(30 * time.Second)
				synctest.Wait()
				if calls[0].Load() != 1 || calls[1].Load() != 1 || !protectionFor(s, key, 1).active(time.Now()) {
					t.Fatal("in-window preparation did not remain stable")
				}
			})
		})
	}
}

func TestIdleQueueCompletedResourceWaitsForRealWindowChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, calls, key := idleBoundaryServer(t, 250, 120*time.Second)
		// A valid playback clock allows the second block to enter at t=5 s.
		release := holdQueuePlaybackUntil(t, s, time.Now())
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		if source, err := s.PrefetchQueuePlayback(t.Context(), 42); err != nil || source != "TEMP" {
			t.Fatal(source, err)
		}
		release()
		time.Sleep(45 * time.Second)
		synctest.Wait()
		s.retentionMu.Lock()
		local := s.localResources[key]
		s.retentionMu.Unlock()
		if calls[1].Load() != 1 || protectionFor(s, key, 1).active(time.Now()) || local != nil && local.has(1) {
			t.Fatal("completed resource refilled outside the idle window", calls[1].Load())
		}
		if _, err := s.usage.db.Exec(`UPDATE songs SET end=240 WHERE id=42`); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls[0].Load() != 1 || calls[1].Load() != 2 || !protectionFor(s, key, 1).active(time.Now()) {
			t.Fatal("completed resource failed to refetch once after metadata changed", calls[0].Load(), calls[1].Load())
		}
	})
}

func TestIdleQueueDownloadAndProtectionWindowsRemainIndependent(t *testing.T) {
	for _, tc := range []struct {
		seconds   int
		protect   time.Duration
		requests  int32
		protected bool
	}{
		{230, 110 * time.Second, 1, false}, // Download once; no protection or refill.
		{250, 130 * time.Second, 0, true},  // Protection alone cannot start a download.
	} {
		t.Run(fmt.Sprint(tc.seconds), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, calls, key := idleBoundaryServer(t, tc.seconds, tc.protect)
				if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
					t.Fatal(err)
				}
				go s.PrefetchQueuePlayback(t.Context(), 42)
				time.Sleep(45 * time.Second)
				synctest.Wait()
				if calls[1].Load() != tc.requests || protectionFor(s, key, 1).active(time.Now()) != tc.protected {
					t.Fatal("download and protection qualifications were coupled", calls[1].Load(), protectionFor(s, key, 1))
				}
			})
		})
	}
}

func TestIdleQueueFutureEstimateTransitionsWithoutWallClockEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.cfg.PrepareAhead, s.cfg.LocalProtectAhead = 120*time.Second, 120*time.Second
		base := time.Now()
		release := holdQueuePlaybackUntil(t, s, base.Add(60*time.Second))
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		registerQueueResource(t, s, "1", w.v.key)
		s.mu.Lock()
		s.prefetchSources["1"].timing = mediaTiming{w.v.size, 2500 * time.Second} // block 1: 125 s
		s.notifyUseChangedLocked(s.retimeQueueLocked(time.Now())...)
		s.mu.Unlock()
		release()
		time.Sleep(59 * time.Second)
		synctest.Wait()
		s.mu.Lock()
		future := s.queueStart
		s.mu.Unlock()
		if future != base.Add(60*time.Second) {
			t.Fatal("future estimate was pulled forward", future)
		}
		// Keep the published snapshot across the old absolute t=65 entry.
		plan, _, _ := w.plan(time.Now(), true)
		if !plan[1].refetchAt(s.protectionAhead()).IsZero() {
			t.Fatal("out-of-window resource retained an absolute refetch timer")
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if !plan[1].need.WindowClosed || protectionFor(s, w.v.key, 1).active(time.Now()) {
			t.Fatal("old future estimate crossed the idle boundary before refresh")
		}
		// A new GET restores absolute timing.
		holdQueuePlaybackUntil(t, s, base.Add(60*time.Second))
		plan, _, _ = w.plan(time.Now(), true)
		if plan[1].need.WindowClosed || !protectionFor(s, w.v.key, 1).active(time.Now()) {
			t.Fatal("new GET failed to restore the fixed playback clock")
		}
	})
}
