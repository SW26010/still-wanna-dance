package cacheproxy

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestSharedLocalHitsAtUsageCapacity(t *testing.T) {
	started := make(chan struct{}, 3)
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	})
	seed := func(target, body string) {
		t.Helper()
		r, _ := http.NewRequest("GET", target, nil)
		v, err := s.parse(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.videoFile(v.key), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	seed(videoURL(payload), payload)
	corrupt := videoURL("also missing")
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parse(r)
	s.mu.Lock()
	s.reserveUseLocked(useSpan{v.key, 0, 1}, true)
	s.reserveUseLocked(useSpan{"occupied", 0, maxUseBlocks - 1}, true)
	s.mu.Unlock()
	for _, tc := range []struct {
		method  string
		headers map[string]string
		status  int
		body    string
	}{
		{"GET", nil, 200, payload},
		{"HEAD", nil, 200, ""},
		{"GET", map[string]string{"Range": "bytes=10-13"}, 206, "abcd"},
	} {
		w := request(s, tc.method, videoURL(payload), tc.headers)
		assertResponse(t, w, tc.status, tc.body)
		if w.Header().Get("X-SWD-Cache") != "HIT" {
			t.Fatal("local response was not a hit", w.Header())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if source, err := s.Prefetch(ctx, videoURL(payload)); err != nil || source != "HIT" {
		t.Fatalf("local prefetch: %q, %v", source, err)
	}
	for _, target := range []string{videoURL("missing"), corrupt} {
		if w := request(s, "GET", target, nil); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("uncached/invalid video bypassed capacity: %d", w.Code)
		}
	}
	select {
	case <-started:
		t.Fatal("local request started an extra download")
	default:
	}
}
