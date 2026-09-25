package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBackgroundCapacitySurvivesCancellationAndLeavesPlaybackSlot(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		started <- r.URL.Path
		select {
		case <-release:
			io.WriteString(w, payload)
		case <-r.Context().Done():
		}
	})
	defer close(release)
	target := func(id string) string { return strings.Replace(videoURL(payload), "1344-", id+"-", 1) }
	waitStart := func() string {
		t.Helper()
		select {
		case path := <-started:
			return path
		case <-time.After(3 * time.Second):
			t.Fatal("download did not start")
			return ""
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 2)
	for _, id := range []string{"1", "2"} {
		go func() { _, err := s.Prefetch(ctx, target(id)); finished <- err }()
	}
	waitStart()
	waitStart()
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("canceled caller did not return")
		}
	}
	// A newly started task must wait while the canceled callers' flights live.
	waitCtx, stopWaiting := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopWaiting()
	if _, err := s.Prefetch(waitCtx, target("3")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected queued prefetch to time out, got %v", err)
	}
	select {
	case path := <-started:
		t.Fatalf("background exceeded two flights: %s", path)
	default:
	}
	// Playback uses the reserved third slot; another request can share a flight.
	playCtx, stopPlay := context.WithCancel(context.Background())
	defer stopPlay()
	playDone := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(playCtx, "GET", target("4"), nil)
		v, _ := s.parse(r)
		_, reader, err := s.obtain(playCtx, v)
		if reader != nil {
			reader.Close()
		}
		playDone <- err
	}()
	if path := waitStart(); !strings.Contains(path, "/4-") {
		t.Fatal(path)
	}
	stopPlay()
	<-playDone
	shareCtx, stopShare := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopShare()
	if _, err := s.Prefetch(shareCtx, target("1")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case path := <-started:
		t.Fatalf("duplicate flight: %s", path)
	default:
	}
	// Waiting requests must also be released when the engine shuts down.
	closed := make(chan error, 1)
	go func() { _, err := s.Prefetch(context.Background(), target("5")); closed <- err }()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not release prefetch")
	}
}
