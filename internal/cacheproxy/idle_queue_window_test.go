package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/vrclog"
)

// Reproduce the review's 125 s block / 120 s window with real local storage,
// controlled HTTP ranges and zero retention budget, without a listening socket.
func idleBoundaryServer(t *testing.T, seconds int) (*Server, *[2]atomic.Int32, string) {
	t.Helper()
	body := strings.Repeat("r", int(2*rangeBlockSize))
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	cfg.CacheUnlimited, cfg.VideoBudgetBytes = false, 0
	cfg.PrepareAhead = 120 * time.Second
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
	if _, err := s.Store.usage.db.Exec(`INSERT INTO songs(id,end) VALUES (42,?)`, seconds); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.usage.db.Exec(`INSERT INTO media(md5,byte_size) VALUES (?,?)`, key, len(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.usage.db.Exec(`INSERT INTO song_media(song_id,md5) VALUES (42,?)`, key); err != nil {
		t.Fatal(err)
	}
	return s, calls, key
}

func TestIdleQueueBoundaryDoesNotEnterBetweenRefreshes(t *testing.T) {
	for _, seconds := range []int{240, 250} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, calls, key := idleBoundaryServer(t, seconds)
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
						want = 1 // The shared window includes its boundary.
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
					if _, err := s.Store.usage.db.Exec(`UPDATE songs SET end=240 WHERE id=42`); err != nil {
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

func TestIdleQueueKeepsCompletedBlocksOutsideWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, calls, key := idleBoundaryServer(t, 240)
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		if source, err := s.PrefetchQueuePlayback(t.Context(), 42); err != nil || source != "TEMP" {
			t.Fatal(source, err)
		}
		if _, err := s.Store.usage.db.Exec(`UPDATE songs SET end=250 WHERE id=42`); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(45 * time.Second)
		synctest.Wait()
		s.retentionMu.Lock()
		local := s.localResources[key]
		s.retentionMu.Unlock()
		if calls[1].Load() != 1 || protectionFor(s, key, 1).active(time.Now()) || local == nil || !local.has(1) {
			t.Fatal("live out-of-window demand lost its block", calls[1].Load())
		}
		if _, err := s.Store.usage.db.Exec(`UPDATE songs SET end=240 WHERE id=42`); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls[0].Load() != 1 || calls[1].Load() != 1 || !protectionFor(s, key, 1).active(time.Now()) {
			t.Fatal("reentry failed to reuse the retained block", calls[0].Load(), calls[1].Load())
		}
		if _, err := s.Store.usage.db.Exec(`UPDATE songs SET end=250 WHERE id=42`); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePrefetchQueue(nil, true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(handoffGrace)
		synctest.Wait()
		if local.has(1) || !protectionFor(s, key, 1).until.IsZero() || calls[1].Load() != 1 {
			t.Fatal("withdrawn out-of-window block survived or was downloaded again")
		}
	})
}

func TestQueueFormalClockExpiryRetainsAbsoluteWindowEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.cfg.PrepareAhead = 120 * time.Second
		base := time.Now()
		setFormalPlaybackEnd(t, s, base.Add(60*time.Second))
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		registerQueueResource(t, s, "1", w.v.key)
		s.mu.Lock()
		s.prefetchSources["1"].timing = mediaTiming{w.v.size, 2500 * time.Second} // block 1: 125 s
		s.notifyUseChangedLocked(s.retimeQueueLocked(time.Now())...)
		s.mu.Unlock()
		time.Sleep(59 * time.Second)
		synctest.Wait()
		s.mu.Lock()
		future := s.queueStart
		s.mu.Unlock()
		if future != base.Add(60*time.Second) {
			t.Fatal("future estimate was pulled forward", future)
		}
		// While the formal clock is active this offset stays outside the rolling window.
		plan, _, _ := w.plan(time.Now())
		if !plan[1].refetchAt().IsZero() {
			t.Fatal("out-of-window resource retained an absolute refetch timer")
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		plan, _, _ = w.plan(time.Now())
		if plan[1].need.WindowClosed || plan[1].refetchAt() != base.Add(65*time.Second) ||
			!protectionFor(s, w.v.key, 1).active(time.Now()) || plan[0].need.ReadyAt(time.Now()) != future {
			t.Fatal("held schedule did not use the previous absolute deadlines")
		}
	})
}

func TestQueueHeadDepartureKeepsPreparedBlockAndDefersLaterBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, calls, key := idleBoundaryServer(t, 240)
		if _, err := s.Store.usage.db.Exec("INSERT INTO songs(id,end) VALUES (1,100)"); err != nil {
			t.Fatal(err)
		}
		end := time.Now().Add(time.Second)
		setFormalPlaybackEnd(t, s, end)
		if err := s.UpdatePrefetchQueue([]int64{1, 42}, false); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := s.PrefetchQueuePlayback(t.Context(), 42); done <- err }()
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if calls[0].Load() != 1 || calls[1].Load() != 0 {
			t.Fatalf("unexpected origin calls before departure: %d, %d", calls[0].Load(), calls[1].Load())
		}
		s.applyPlaybackTimeline(vrclog.PlayerTimeline{}, true) // The old anchor was retired before the snapshot.
		if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
			t.Fatal(err)
		}
		s.applyPlaybackTimeline(vrclog.PlayerTimeline{
			Anchor: vrclog.PlaybackAnchor{SongID: 99, At: time.Now()},
		}, true) // No media mapping for this replacement.
		synctest.Wait()
		s.requestBlockReclamation(key)
		synctest.Wait()
		r := httptest.NewRequest(http.MethodGet, "https://api.udon.dance/Api/Songs/play?id=42", nil)
		r.Header.Set("Range", "bytes=0-0")
		response := httptest.NewRecorder()
		s.ServeHTTP(response, r)
		if response.Code != http.StatusPartialContent || response.Body.String() != "r" ||
			calls[0].Load() != 1 || calls[1].Load() != 0 || !protectionFor(s, key, 0).active(time.Now()) {
			t.Fatalf("head departure altered the window or discarded ready data: status=%d calls=%d,%d", response.Code, calls[0].Load(), calls[1].Load())
		}
		time.Sleep(100*time.Second - time.Nanosecond)
		synctest.Wait()
		if calls[1].Load() != 0 {
			t.Fatal("second block started before its original entry")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if calls[1].Load() != 1 || calls[0].Load() != 1 {
			t.Fatalf("fixed entry did not release exactly one later block: %d,%d", calls[0].Load(), calls[1].Load())
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
