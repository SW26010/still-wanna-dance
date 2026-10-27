package console

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestPlaybackJoinedChannelFailuresRetainGatewayTimeout(t *testing.T) {
	c := testConsole(t)
	refused := errors.New("connection refused")
	var calls atomic.Int32
	channels := &coldBusinessChannels{ready: true}
	for i, cause := range []error{refused, context.DeadlineExceeded, nil} {
		channels.channels = append(channels.channels, upstreamrequest.Candidate{
			ID: []string{"refused", "timeout", "unavailable"}[i], Host: "api.udon.dance", Mode: "direct",
			Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.URL.Query().Get("node") != "cf" {
					t.Errorf("unexpected source: %s", r.URL)
				}
				if cause != nil {
					return nil, cause
				}
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
			}),
		})
	}
	c.client.Transport = channels
	cfg := fixtureCacheConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	resolved := make(chan error, 1)
	cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
		target, err := c.resolvePlayback(ctx, id)
		resolved <- err
		return target, err
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://api.udon.dance/Api/Songs/play?id=42&node=nya", nil).WithContext(ctx))
	if w.Code != http.StatusGatewayTimeout || ctx.Err() != nil || calls.Load() != 3 {
		t.Errorf("status=%d context=%v calls=%d", w.Code, ctx.Err(), calls.Load())
	}
	select {
	case err := <-resolved:
		if !errors.Is(err, refused) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "503") {
			t.Fatalf("lost channel failures: %v", err)
		}
		if strings.Contains(err.Error(), "https://") {
			t.Fatalf("request URL leaked: %v", err)
		}
	default:
		t.Fatal("playback resolver was not called")
	}
}
