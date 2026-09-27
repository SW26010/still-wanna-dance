package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlaybackCheckSurvivesClientCancellationAndStopsOnClose(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	entered := make(chan context.Context, 1)
	var calls atomic.Int32
	s.cfg.ResolvePlayback = func(ctx context.Context, _, _ string) (string, error) {
		calls.Add(1)
		entered <- ctx
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx)
	done := make(chan error, 1)
	go func() { _, err := s.requestVideo(r, nil); done <- err }()
	var checkCtx context.Context
	select {
	case checkCtx = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("check did not start")
	}
	deadline, ok := checkCtx.Deadline()
	if remaining := time.Until(deadline); !ok || remaining < 25*time.Second || remaining > 30*time.Second {
		t.Fatalf("update check deadline: %v, present=%v", remaining, ok)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("client cancellation ignored", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request kept waiting")
	}
	if checkCtx.Err() != nil {
		t.Fatal("client canceled background check", checkCtx.Err())
	}
	// Another probe shares the pending check even after the first client left.
	local, err := s.localPlaybackVideo(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	a := s.startPlaybackCheck(r.URL.Query(), local)
	b := s.startPlaybackCheck(r.URL.Query(), local)
	if a != b || calls.Load() != 1 {
		t.Fatal("duplicate upstream checks", calls.Load())
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil || !errors.Is(checkCtx.Err(), context.Canceled) {
			t.Fatal("shutdown did not cancel check", err, checkCtx.Err())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked on check")
	}
}

func TestPlaybackCachedResolutionDeadlineAndUpdate(t *testing.T) {
	var body atomic.Value
	body.Store(payload)
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		io.WriteString(w, body.Load().(string))
	})
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	const updated = "updated after slow upstream resolution"
	release := make(chan struct{}, 1)
	defer close(release)
	resolverContext := make(chan context.Context, 1)
	var resolutions atomic.Int32
	s.cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
		resolutions.Add(1)
		resolverContext <- ctx
		select {
		case <-release:
			return videoURL(updated), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s.cfg.ResolveCurrent = func(ctx context.Context, _ string) (string, error) {
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining < 25*time.Second || remaining > 30*time.Second {
			t.Errorf("background confirmation deadline: %v, present=%v", remaining, ok)
		}
		return videoURL(updated), nil
	}
	target := "http://api.udon.dance/Api/Songs/play?id=00042&node=nya"
	started := time.Now()
	w := request(s, "GET", target, nil)
	assertResponse(t, w, 200, payload)
	if elapsed := time.Since(started); elapsed < 5*time.Second || elapsed > 8*time.Second {
		t.Fatalf("cached fallback took %v", elapsed)
	}
	ctx := <-resolverContext
	deadline, ok := ctx.Deadline()
	if remaining := time.Until(deadline); !ok || remaining < 20*time.Second || ctx.Err() != nil {
		t.Fatalf("foreground fallback canceled update check: remaining=%v error=%v", remaining, ctx.Err())
	}
	if w.Header().Get("X-StepStash-Fallback") != "upstream-unavailable" || downloads.Load() != 1 {
		t.Fatal("timeout did not serve local fallback", w.Header(), downloads.Load())
	}
	// The same slow check completes after the foreground response has finished.
	body.Store(updated)
	release <- struct{}{}
	s.wg.Wait()
	assertPlaybackSong(t, s, "42", updated)
	if resolutions.Load() != 1 || downloads.Load() != 2 {
		t.Fatal("background refresh failed", resolutions.Load(), downloads.Load())
	}
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "", errors.New("offline") }
	w = request(s, "GET", target, nil)
	assertResponse(t, w, 200, updated)
}

func TestPlaybackLocalFallback(t *testing.T) {
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		io.WriteString(w, payload)
	})
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	for _, failure := range []string{"network", "timeout", "invalid redirect"} {
		t.Run(failure, func(t *testing.T) {
			s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
				switch failure {
				case "network":
					return "", errors.New("offline")
				case "timeout":
					return "", context.DeadlineExceeded
				default:
					return "https://untrusted.example/video.mp4", nil
				}
			}
			for _, tc := range []struct {
				method, rangeValue, body string
				status                   int
			}{
				{"GET", "", payload, 200},
				{"GET", "bytes=2-5", payload[2:6], 206},
				{"HEAD", "bytes=2-5", "", 200},
			} {
				w := request(s, tc.method, "http://api.udon.dance/Api/Songs/play?id=00042&node=nya", map[string]string{"Range": tc.rangeValue})
				assertResponse(t, w, tc.status, tc.body)
				if w.Header().Get("X-StepStash-Fallback") != "upstream-unavailable" || w.Header().Get("X-StepStash-Cache") != "HIT" {
					t.Fatal("missing fallback/cache headers", w.Header())
				}
				if tc.status == 206 && w.Header().Get("Content-Range") != "bytes 2-5/36" {
					t.Fatal("incorrect range", w.Header())
				}
			}
		})
	}
	assertPlaybackSong(t, s, "42", payload)
	if downloads.Load() != 1 {
		t.Fatal("fallback contacted video upstream", downloads.Load())
	}
	// Once resolution recovers, normal playback must resume.
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(payload), nil }
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
	assertResponse(t, w, 200, payload)
	if w.Header().Get("X-StepStash-Fallback") != "" {
		t.Fatal("fallback persisted after recovery")
	}
}

func TestPlaybackLocalFallbackRejectsUnusableCache(t *testing.T) {
	for _, state := range []string{"missing", "truncated", "corrupt", "unassociated", "other song", "oversize"} {
		t.Run(state, func(t *testing.T) {
			var downloads atomic.Int32
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				io.WriteString(w, payload)
			})
			var err error
			if state == "unassociated" {
				_, err = s.Prefetch(context.Background(), videoURL(payload))
			} else {
				id := "42"
				if state == "other song" {
					id = "43"
				}
				_, err = s.PrefetchSong(context.Background(), id, videoURL(payload))
			}
			if err != nil {
				t.Fatal(err)
			}
			s.wg.Wait()
			path := testVideoFile(t, cfg, payload)
			switch state {
			case "missing":
				err = os.Remove(path)
			case "truncated":
				err = os.WriteFile(path, []byte(payload[:5]), 0600)
			case "corrupt":
				err = os.WriteFile(path, []byte(strings.Repeat("x", len(payload))), 0600)
			case "oversize":
				s.cfg.MaxFileBytes = 1
			}
			if err != nil {
				t.Fatal(err)
			}
			s.cfg.ResolvePlayback = func(ctx context.Context, _, _ string) (string, error) {
				deadline, ok := ctx.Deadline()
				if remaining := time.Until(deadline); !ok || remaining < 25*time.Second || remaining > 30*time.Second {
					t.Fatalf("unusable cache shortened resolution deadline: %v, present=%v", remaining, ok)
				}
				return "", errors.New("offline")
			}
			w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
			if w.Code != http.StatusBadGateway || w.Header().Get("X-StepStash-Fallback") != "" {
				t.Fatal("unusable fallback accepted", w.Code, w.Header())
			}
			if downloads.Load() != 1 {
				t.Fatal("fallback attempted download")
			}
		})
	}
}
