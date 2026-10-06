package console

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestBusinessResolutionReservesFallbackDeadline(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "auto"
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
	var business atomic.Bool
	var attempts atomic.Int32
	measured := catalogTransport(func(r *http.Request) (*http.Response, error) {
		if business.Load() && attempts.Add(1) == 1 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))}
		if r.URL.Path == "/Api/Songs/play" {
			resp.StatusCode = 302
			resp.Header.Set("Location", target)
		}
		return resp, nil
	})
	c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{measured})
	m, err := upstreamstate.NewMonitor(upstreamstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.monitor = m
	if err := m.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
		t.Fatal(err)
	}
	if n := len(c.operationClients(upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: "cf", Entry: c.apiBase + "/Api/Songs/play?node=cf"})); n != 2 {
		t.Fatalf("want two measured channels, got %d", n)
	}
	business.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := c.resolvePlayback(ctx, "42", "cf", "cf")
	if err != nil || got != target || attempts.Load() != 2 {
		t.Fatalf("target=%q attempts=%d err=%v", got, attempts.Load(), err)
	}
}

func TestBusinessResolutionUsesMeasuredTransport(t *testing.T) {
	c := testConsole(t)
	cfg := fixtureCacheConfig()
	cfg.StorageDir = c.settings.StorageDir
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
	measured := catalogTransport(func(r *http.Request) (*http.Response, error) {
		time.Sleep(time.Millisecond)
		resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))}
		if r.URL.Path == "/Api/Songs/play" {
			resp.StatusCode = 302
			resp.Header.Set("Location", target)
		}
		return resp, nil
	})
	c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{measured})
	m, err := upstreamstate.NewMonitor(upstreamstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.monitor = m
	if err := m.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
		t.Fatal(err)
	}
	if len(m.Selections(upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: "cf"})) == 0 {
		t.Fatal("fixture has no measured playback channel")
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Error("business request reselected an unmeasured transport")
		return nil, errors.New("unmeasured transport")
	})
	got, err := c.resolvePlayback(context.Background(), "42", "cf", "cf")
	if err != nil || got != target {
		t.Fatalf("target=%q err=%v", got, err)
	}
	urls, err := engine.SongURLs(context.Background(), 42, "cf")
	if err != nil || len(urls) != 1 || urls[0].URL != target || urls[0].API != c.apiBase+"/Api/Songs/play" {
		t.Fatalf("API observation not persisted: %+v %v", urls, err)
	}
}
