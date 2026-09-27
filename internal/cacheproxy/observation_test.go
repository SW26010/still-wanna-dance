package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestPlaybackArrivalObservedBeforeUpstreamResolution(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
			var arrivals []time.Time
			s.cfg.BeginVideoRequest = func(at time.Time) func() {
				return func() { arrivals = append(arrivals, at) }
			}
			resolverCalls := 0
			fail := true
			s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
				resolverCalls++
				if len(arrivals) != resolverCalls {
					t.Errorf("arrival not recorded before resolver: arrivals=%d calls=%d", len(arrivals), resolverCalls)
				}
				if fail {
					return "", errors.New("isolated upstream failure")
				}
				return videoURL(payload), nil
			}
			for _, target := range []string{
				"http://api.udon.dance/Api/Songs/play",
				"http://api.udon.dance/Api/Songs/play?id=0",
				"http://api.udon.dance/Api/Songs/play?id=1&id=2",
				"http://api.udon.dance/Api/Songs/play?id=1&node=bad",
				"http://api.udon.dance/Api/Songs/play?id=1&node=cf&node=nya",
				"http://api.udon.dance/Api/Songs/play?id=%zz",
				"http://api.udon.dance/Api/Songs/%70lay?id=1",
				"http://unknown.test/Api/Songs/play?id=1",
			} {
				if w := request(s, method, target, nil); w.Code != 400 {
					t.Errorf("invalid request %s: %d", target, w.Code)
				}
			}
			request(s, "POST", "http://api.udon.dance/Api/Songs/play?id=1", nil)
			request(s, method, "http://api.udon.dance/Api/Songs/list", nil)
			if len(arrivals) != 0 || resolverCalls != 0 {
				t.Fatal("invalid/non-playback request counted")
			}
			before := time.Now()
			if w := request(s, method, "http://api.udon.dance/Api/Songs/play?id=1&node=cf", nil); w.Code != 502 {
				t.Fatal("expected resolution failure without old cache", w.Code)
			}
			if len(arrivals) != 1 || arrivals[0].Before(before) {
				t.Fatal("failed playback arrival missing", arrivals)
			}
			fail = false
			if w := request(s, method, "http://api.udon.dance/Api/Songs/play?id=1", nil); w.Code != 200 {
				t.Fatal("expected resolved video", w.Code)
			}
			if len(arrivals) != 2 {
				t.Fatal("successful playback counted more than once", arrivals)
			}
		})
	}
}

func TestVideoObservationExcludesPrefetchAndNonVideo(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	var arrivals []time.Time
	s.cfg.BeginVideoRequest = func(at time.Time) func() { return func() { arrivals = append(arrivals, at) } }
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	request(s, "GET", "http://api.udon.dance/Api/Songs/list", nil)
	request(s, "GET", "http://play.udon.dance/api/status", nil)
	request(s, "POST", videoURL(payload), nil)
	if len(arrivals) != 0 {
		t.Fatal("non-video or prefetch counted", arrivals)
	}
	before := time.Now()
	request(s, "HEAD", videoURL(payload), nil)
	request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=0-1"})
	if len(arrivals) != 2 || arrivals[0].Before(before) || arrivals[1].Before(arrivals[0]) {
		t.Fatal("expected inbound video arrival times", arrivals)
	}
}
