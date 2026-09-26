package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

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
			s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "", errors.New("offline") }
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
