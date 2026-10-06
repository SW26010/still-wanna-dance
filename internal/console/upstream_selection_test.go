package console

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestBusinessResolutionUsesMeasuredTransport(t *testing.T) {
	c := testConsole(t)
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
}
