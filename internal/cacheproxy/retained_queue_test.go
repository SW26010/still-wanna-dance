package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetainedQueueRefetchAfterEviction(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		hit, timed, changedAddress bool
	}{
		{name: "published then reordered"},
		{name: "HIT then reordered", hit: true},
		{name: "published then enters by time", timed: true},
		{name: "HIT then enters by time", hit: true, timed: true},
		{name: "HIT address changed identity", hit: true, changedAddress: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, other := payload, strings.Repeat("z", len(payload))
			otherURL, _ := url.Parse(videoURL(other))
			cfg := fixtureConfig()
			cfg.StorageDir = t.TempDir()
			cfg.CacheUnlimited, cfg.MaxCacheBytes = false, int64(len(body))
			cfg.PrepareAhead, cfg.LocalProtectAhead = 120*time.Second, 120*time.Second
			var changed atomic.Bool
			cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
				if changed.Load() {
					return videoURL(other), nil
				}
				return videoURL(body), nil
			}
			var calls atomic.Int32
			cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					content := body
					if r.URL.Query().Get("e") == otherURL.Query().Get("e") {
						content = other
					}
					var start, end int64
					if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: http.StatusPartialContent, Request: r,
						ContentLength: end - start + 1, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(content))}},
						Body: io.NopCloser(strings.NewReader(content[start : end+1]))}, nil
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
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			if tc.hit {
				if _, err := s.PrefetchSong(ctx, "42", videoURL(body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
				t.Fatal(err)
			}
			source, err := s.PrefetchQueuePlayback(ctx, 42)
			if err != nil || tc.hit && source != "HIT" || !tc.hit && source != "MISS" {
				t.Fatal("initial preparation", source, err)
			}
			v := parsedVideo(t, s, body)
			expectRetained(t, cfg.videoFile(v.key), true)
			s.mu.Lock()
			original := s.prefetchSources["42"]
			entry := time.Now().Add(500 * time.Millisecond)
			if tc.timed {
				s.queueStart = entry.Add(s.protectionAhead() - typicalDuration)
			}
			s.mu.Unlock()
			if err := s.UpdatePrefetchQueue([]int64{1, 42}, false); err != nil {
				t.Fatal(err)
			}
			if protectionFor(s, v.key, 0).active(time.Now()) {
				t.Fatal("future item still protected")
			}
			// Let coordination observe the retained file before eviction. Timed
			// cases receive no later queue update to discover its disappearance.
			time.Sleep(50 * time.Millisecond)
			hotter := parsedVideo(t, s, other)
			seedPrioritySong(t, s, "43", hotter.key)
			if err := s.usage.write([]usageEvent{{id: "43", at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PrefetchSong(ctx, "43", videoURL(other)); err != nil {
				t.Fatal("higher-priority replacement", err)
			}
			s.trimCache()
			expectRetained(t, cfg.videoFile(v.key), false)
			if calls.Load() != 2 {
				t.Fatal("future usage downloaded outside its window", calls.Load())
			}
			changed.Store(tc.changedAddress)
			if !tc.timed {
				if err := s.UpdatePrefetchQueue([]int64{42, 1}, false); err != nil {
					t.Fatal(err)
				}
			}
			if tc.changedAddress {
				deadline := time.Now().Add(3 * time.Second)
				for {
					s.mu.Lock()
					stopped := s.resourceUses[v.key] == nil && s.flights[v.key] == nil
					s.mu.Unlock()
					if stopped {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("changed resource resolution did not stop")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if calls.Load() != 2 {
					t.Fatal("downloaded different content for existing usage", calls.Load())
				}
				return
			}
			waitQueueRefetch(t, s, v, &calls, 3)
			if tc.timed && time.Now().Before(entry) {
				t.Fatal("downloaded before protection entry")
			}
			s.mu.Lock()
			valid := s.prefetchSources["42"] == original && original.use.contains(v.key, 0) && !original.expired && len(s.demands) == 0
			s.mu.Unlock()
			if !valid {
				t.Fatal("refetch changed source identity or left synthetic usage")
			}
			// The persistent high-priority file stays; recovered temporary blocks
			// must still serve the queued resource without another upstream request.
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, videoURL(body), nil))
			if w.Code != http.StatusOK || w.Body.String() != body || calls.Load() != 3 {
				t.Fatal("playback did not reuse recovered content", w.Code, calls.Load())
			}
		})
	}
}
