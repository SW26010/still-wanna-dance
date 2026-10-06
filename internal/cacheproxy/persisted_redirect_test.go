package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistedURLRedirectTrustIsTaskScoped(t *testing.T) {
	for _, kind := range []string{"same-host", "cross-host", "changed-content"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg := setup(t, nil)
			target := strings.Replace(videoURL(payload), "play.udon.dance", "media.example", 1)
			at := time.Now().Add(-time.Minute)
			if err := s.ObserveSongURL(context.Background(), SongURL{SongID: 42, URL: target, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: at, ObservedAt: at}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			cfg.IsResourceHost = func(string) bool { return false }
			cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "", errors.New("API unavailable") }
			var bodies atomic.Int32
			cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Query().Get("redirected") == "1" {
						bodies.Add(1)
						return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
					}
					next := target + "&redirected=1"
					if kind == "cross-host" {
						next = strings.Replace(next, "media.example", "other.example", 1)
					}
					if kind == "changed-content" {
						next = strings.Replace(next, "s=36", "s=37", 1)
					}
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {next}}, Body: http.NoBody, Request: r}, nil
				})}
			}
			var err error
			s, err = New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
			if kind == "same-host" {
				if w.Code != 200 || w.Body.String() != payload || bodies.Load() != 1 {
					t.Fatalf("status=%d bodies=%d", w.Code, bodies.Load())
				}
			} else if w.Code != 502 || bodies.Load() != 0 {
				t.Fatalf("untrusted redirect accepted: status=%d bodies=%d", w.Code, bodies.Load())
			}
			if w := request(s, "GET", target, nil); w.Code != 400 {
				t.Fatalf("inbound permissions widened: %d", w.Code)
			}
		})
	}
}
