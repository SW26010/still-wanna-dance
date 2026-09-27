package cacheproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlaybackCanceledProbeRefreshesFastResult(t *testing.T) {
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
	entered, resume := make(chan struct{}), make(chan struct{})
	const updated = "new confirmed video"
	body.Store(updated)
	s.cfg.ResolvePlayback = func(ctx context.Context, _, _ string) (string, error) {
		close(entered)
		select {
		case <-resume:
			return videoURL(updated), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s.cfg.ResolveCurrent = func(context.Context, string) (string, error) { return videoURL(updated), nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx)
	done := make(chan error, 1)
	go func() {
		_, release, err := s.requestVideo(r, nil)
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("check did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not cancel")
	}
	close(resume)
	s.wg.Wait()
	assertPlaybackSong(t, s, "42", updated)
	if downloads.Load() != 2 {
		t.Fatalf("downloads=%d", downloads.Load())
	}
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "", errors.New("offline") }
	assertResponse(t, request(s, "GET", r.URL.String(), nil), 200, updated)
}

type playbackHandoffLog struct {
	slog.Handler
	hook func()
}

func (h playbackHandoffLog) WithAttrs(a []slog.Attr) slog.Handler {
	return playbackHandoffLog{h.Handler.WithAttrs(a), h.hook}
}
func (h playbackHandoffLog) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "request_started" {
		h.hook()
	}
	return h.Handler.Handle(ctx, r)
}

func TestPlaybackVerifiedPinSurvivesHandoff(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "invalid-range"} {
		t.Run(method, func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
			if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			s.wg.Wait()
			local, err := s.localPlaybackVideo(context.Background(), "42")
			if err != nil {
				t.Fatal(err)
			}
			entry, err := readCacheEntry(context.Background(), s.cfg.StorageDir, local.key, s.usage.db)
			if err != nil {
				t.Fatal(err)
			}
			selection := []CacheSelection{{Key: entry.Key, Stamp: entry.Stamp}}
			s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "", errors.New("offline") }
			called := false
			s.cfg.Logger = slog.New(playbackHandoffLog{s.cfg.Logger.Handler(), func() {
				called = true
				// The resolver's final defer releases its pin before removing this entry.
				deadline := time.Now().Add(time.Second)
				for {
					s.mu.Lock()
					pending := len(s.playbackChecks)
					s.mu.Unlock()
					if pending == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("resolver did not finish")
					}
					time.Sleep(time.Millisecond)
				}
				removed, err := s.DeleteCache(context.Background(), selection)
				if err != nil || len(removed) != 1 || removed[0].Result != "protected" {
					t.Fatalf("handoff deletion: %+v %v", removed, err)
				}
			}})
			headers := map[string]string{}
			verb := method
			expected := 200
			if method == "invalid-range" {
				verb = "GET"
				headers["Range"] = "bytes=0-1,3-4"
				expected = 416
			}
			w := request(s, verb, "http://api.udon.dance/Api/Songs/play?id=42", headers)
			if !called || w.Code != expected {
				t.Fatalf("called=%v status=%d", called, w.Code)
			}
			s.wg.Wait()
			removed, err := s.DeleteCache(context.Background(), selection)
			if err != nil || len(removed) != 1 || removed[0].Result != "deleted" {
				t.Fatalf("pin leaked after response: %+v %v", removed, err)
			}
		})
	}
}
