package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func completedQueueFixture(t *testing.T, body string, before func(*http.Request) error) (*Server, video, *queueSource, *atomic.Int32) {
	t.Helper()
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	cfg.CacheUnlimited, cfg.MaxCacheBytes = false, 0
	cfg.PrepareAhead, cfg.LocalProtectAhead = 120*time.Second, 120*time.Second
	cfg.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(body), nil }
	calls := new(atomic.Int32)
	cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if before != nil {
				if err := before(r); err != nil {
					return nil, err
				}
			}
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
		t.Fatal(err)
	}
	if source, err := s.PrefetchQueuePlayback(ctx, 42); err != nil || source != "TEMP" {
		t.Fatalf("initial preparation: source=%q err=%v", source, err)
	}
	v := parsedVideo(t, s, body)
	s.mu.Lock()
	q := s.prefetchSources["42"]
	valid := q.use.contains(v.key, 0) && s.flights[v.key] == nil && len(s.demands) == 0
	s.mu.Unlock()
	if !valid {
		t.Fatal("preparation must finish while retaining only queue usage")
	}
	return s, v, q, calls
}

func waitQueueRefetch(t *testing.T, s *Server, v video, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		idle := s.flights[v.key] == nil
		s.mu.Unlock()
		s.retentionMu.Lock()
		local := s.localResources[v.key]
		complete := local != nil && local.complete()
		s.retentionMu.Unlock()
		if idle && complete && calls.Load() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("current queue usage did not recover: calls=%d want=%d idle=%v complete=%v", calls.Load(), want, idle, complete)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func moveCompletedQueueOut(t *testing.T, s *Server, v video) {
	t.Helper()
	// The same member stays in the queue, but the ordinary
	// predecessor's 210-second duration moves its need outside both windows.
	if err := s.UpdatePrefetchQueue([]int64{1, 42}, false); err != nil {
		t.Fatal(err)
	}
	if protectionFor(s, v.key, 0).active(time.Now()) {
		t.Fatal("reordered usage incorrectly kept immediate protection")
	}
	s.reclaimLocalBlocks()
	s.retentionMu.Lock()
	local := s.localResources[v.key]
	available := local != nil && local.has(0)
	s.retentionMu.Unlock()
	if available {
		t.Fatal("unprotected zero-budget block was not reclaimed")
	}
}

func TestCompletedQueueReentryAfterReclamation(t *testing.T) {
	s, v, q, calls := completedQueueFixture(t, payload, nil)
	for cycle := int32(1); cycle <= 2; cycle++ {
		moveCompletedQueueOut(t, s, v)
		time.Sleep(50 * time.Millisecond)
		if calls.Load() != cycle {
			t.Fatal("future usage refilled an unprotected block", calls.Load())
		}
		for range 3 {
			if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
				t.Fatal(err)
			}
		}
		waitQueueRefetch(t, s, v, calls, cycle+1)
		s.mu.Lock()
		valid := s.prefetchSources["42"] == q && q.use.contains(v.key, 0) && !q.expired && len(s.demands) == 0
		s.mu.Unlock()
		if !valid {
			t.Fatal("refetch replaced the source or created synthetic usage")
		}
	}
	// No GET was needed to recover availability. Actual playback now reuses it.
	r := httptest.NewRequest(http.MethodGet, videoURL(payload), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != payload || calls.Load() != 3 {
		t.Fatal("playback fell back to a cold download", w.Code, calls.Load())
	}
}

func TestCompletedQueueRefetchWindowAndWithdrawal(t *testing.T) {
	for _, action := range []string{"enter by time", "remove", "reset"} {
		t.Run(action, func(t *testing.T) {
			s, v, _, calls := completedQueueFixture(t, payload, nil)
			entry := time.Now().Add(300 * time.Millisecond)
			holdQueuePlaybackUntil(t, s, entry.Add(s.protectionAhead()-typicalDuration))
			moveCompletedQueueOut(t, s, v)
			if action == "enter by time" {
				waitQueueRefetch(t, s, v, calls, 2)
				if time.Now().Before(entry) {
					t.Fatal("refetched before the protection window")
				}
				return
			}
			ids := []int64{1}
			if action == "reset" {
				ids = []int64{1, 42}
			}
			if err := s.UpdatePrefetchQueue(ids, action == "reset"); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Until(entry) + 100*time.Millisecond)
			s.mu.Lock()
			stopped := s.resourceUses[v.key] == nil && s.flights[v.key] == nil
			s.mu.Unlock()
			if calls.Load() != 1 || !stopped {
				t.Fatal("withdrawn source left refetch work or revived after entry", calls.Load(), stopped)
			}
		})
	}
}

func TestCompletedQueueRefetchSharesGETAndAvailableBlocks(t *testing.T) {
	body := strings.Repeat("r", int(2*rangeBlockSize))
	gate, refetch := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(gate) })
	defer unblock()
	var first atomic.Int32
	s, v, _, calls := completedQueueFixture(t, body, func(r *http.Request) error {
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") && first.Add(1) == 2 {
			close(refetch)
			select {
			case <-gate:
			case <-r.Context().Done():
				return r.Context().Err()
			}
		}
		return nil
	})
	// An independent GET keeps only the second block protected through reorder.
	ctx := context.WithValue(t.Context(), httpGETKey{}, true)
	ctx = context.WithValue(ctx, playbackClockKey{}, &playbackClock{
		at: time.Now(), offset: rangeBlockSize, limit: v.size, media: estimateMediaTiming(v.size, 8),
	})
	_, release := addTestDemand(t, s, ctx, v, true)
	defer release()
	moveCompletedQueueOut(t, s, v)
	if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
		t.Fatal(err)
	}
	receiveDemandEvent(t, refetch)
	s.mu.Lock()
	shared := s.flights[v.key]
	s.mu.Unlock()
	getCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	f, reader, err := s.obtain(getCtx, v)
	if err != nil || f != shared || reader == nil {
		t.Fatal("GET did not share the automatic refetch", err)
	}
	defer reader.Close()
	unblock()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != body {
		t.Fatal("shared GET did not read the complete resource", err)
	}
	waitQueueRefetch(t, s, v, calls, 3)
}

func TestCompletedQueueRefetchFailureDoesNotLoop(t *testing.T) {
	var requests atomic.Int32
	s, v, _, calls := completedQueueFixture(t, payload, func(*http.Request) error {
		if requests.Add(1) > 1 {
			return errors.New("refetch origin unavailable")
		}
		return nil
	})
	moveCompletedQueueOut(t, s, v)
	if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		stopped := s.resourceUses[v.key] == nil && s.flights[v.key] == nil
		s.mu.Unlock()
		if stopped && calls.Load() > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed refetch did not finish its bounded attempts", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := calls.Load()
	if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("unchanged usage retried a terminal failure indefinitely", calls.Load())
	}
}

func TestCompletedQueueRefetchExpiryRearmsPreparation(t *testing.T) {
	s, v, q, calls := completedQueueFixture(t, payload, nil)
	if s.QueueNeedsPreparation(42) {
		t.Fatal("completed preparation still has valid queue usage")
	}
	moveCompletedQueueOut(t, s, v)
	releaseClock := holdQueuePlaybackUntil(t, s, time.Now().Add(-blocksched.DownloadGrace-time.Second))
	if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		expired := q.expired && q.use == (useSpan{}) && s.flights[v.key] == nil && s.resourceUses[v.key] == nil
		s.mu.Unlock()
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic refetch did not withdraw its expired queue usage")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
		t.Fatal(err)
	}
	if s.QueueNeedsPreparation(42) || calls.Load() != 1 {
		t.Fatal("unchanged expired member was retried", calls.Load())
	}
	changed := s.QueuePreparationChanged()
	// An actual GET changes the queue's playback anchor and rearms preparation.
	releaseClock()
	ctx := context.WithValue(t.Context(), playbackClockKey{}, &playbackClock{
		at: time.Now(), limit: 1, media: estimateMediaTiming(1, 1),
	})
	_, release := addTestDemand(t, s, ctx, video{key: "playing", size: 1}, true)
	defer release()
	if !s.QueueNeedsPreparation(42) {
		t.Fatal("clock change did not expose the missing preparation")
	}
	select {
	case <-changed:
	default:
		t.Fatal("rearmed preparation did not notify the queue coordinator")
	}
	if source, err := s.PrefetchQueuePlayback(t.Context(), 42); err != nil || source != "TEMP" {
		t.Fatal("rearmed member could not prepare", source, err)
	}
	waitQueueRefetch(t, s, v, calls, 2)
	if s.QueueNeedsPreparation(42) {
		t.Fatal("restored usage requested another preparation")
	}
}
