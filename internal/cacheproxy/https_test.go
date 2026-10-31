package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPOriginUsesIndependentDialAndPublicName(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			t.Error("resource request used TLS")
		}
		if r.Host == "nya.xin.moe" {
			w.Header().Set("Location", videoURL(payload))
			w.WriteHeader(302)
			return
		}
		if r.Host != "play.udon.dance" {
			t.Errorf("unexpected host %s", r.Host)
		}
		io.WriteString(w, payload)
	}))
	defer backend.Close()
	for _, override := range []string{"", "origin.example:8080"} {
		t.Run(override, func(t *testing.T) {
			cfg := fixtureConfig()
			prepareFixtureResources(t, &cfg, t.TempDir(), true)
			if override != "" {
				for host := range cfg.Origins {
					cfg.Origins[host] = override
				}
			}
			var dials atomic.Int32
			cfg.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				if override != "" && address != override {
					t.Errorf("override not used: %s", address)
				} else if override == "" && address != "play.udon.dance:80" && address != "nya.xin.moe:80" {
					t.Errorf("unexpected origin %s", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			target := strings.Replace(videoURL(payload), "play.udon.dance", "nya.xin.moe", 1)
			assertResponse(t, request(s, "GET", target, nil), 200, payload)
			if dials.Load() != 2 {
				t.Fatal("missing independent dial", dials.Load())
			}
		})
	}
}

func TestUpstreamDialFailureNeverFallsBack(t *testing.T) {
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	sentinel := errors.New("built-in DNS unavailable")
	var calls atomic.Int32
	cfg.DialContext = func(context.Context, string, string) (net.Conn, error) { calls.Add(1); return nil, sentinel }
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Prefetch(context.Background(), videoURL(payload)); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retries", calls.Load())
	}
	transport := newTransport()
	if _, err := transport.DialContext(context.Background(), "tcp", "localhost:80"); err == nil {
		t.Fatal("system DNS was allowed")
	}
}

func TestPlaybackAPIStreamsAndReusesVideoCache(t *testing.T) {
	var downloads atomic.Int32
	var invalidRedirect atomic.Bool
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); io.WriteString(w, payload) }, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			if invalidRedirect.Load() {
				return "https://evil.invalid/video", nil
			}
			if id != "42" {
				t.Errorf("id=%s", id)
			}
			return strings.Replace(videoURL(payload), "http:", "https:", 1), nil
		}
	})
	target := "http://api.udon.dance/Api/Songs/play?id=42&node=cf"
	assertResponse(t, request(s, "GET", target, nil), 200, payload)
	assertResponse(t, request(s, "GET", target, map[string]string{"Range": "bytes=2-5"}), 206, payload[2:6])
	assertResponse(t, request(s, "HEAD", target, nil), 200, "")
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	if downloads.Load() != 1 {
		t.Fatalf("downloaded %d times", downloads.Load())
	}
	for _, query := range []string{"id=0", "id=42&id=43", "id=42&node=evil", "id=42&node=cf&node=nya", "id=42;bad=1"} {
		if w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?"+query, nil); w.Code != 400 {
			t.Errorf("%s: %d", query, w.Code)
		}
	}
	invalidRedirect.Store(true)
	assertResponse(t, request(s, "GET", target, nil), 200, payload)
	if w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=43", nil); w.Code != 502 {
		t.Fatal("unknown song accepted invalid redirect", w.Code)
	}
	if downloads.Load() != 1 {
		t.Fatal("invalid redirect triggered download", downloads.Load())
	}
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/list", nil)
	if w.Code != 307 || w.Header().Get("Location") != "https://api.udon.dance/Api/Songs/list" {
		t.Fatal(w.Code, w.Header())
	}
}

func TestVideoRedirectRejectsChangedContentAndForeignHosts(t *testing.T) {
	for _, target := range []string{strings.Replace(videoURL(payload), "play.udon.dance", "evil.invalid", 1), videoURL("changed content")} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target)
				w.WriteHeader(302)
			})
			if w := request(s, "GET", videoURL(payload), nil); w.Code != 502 {
				t.Fatal(w.Code)
			}
			if calls.Load() != 1 {
				t.Fatal("followed invalid redirect", calls.Load())
			}
		})
	}
}
