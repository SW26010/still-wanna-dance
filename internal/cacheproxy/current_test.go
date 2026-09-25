package cacheproxy

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBlockedConfirmationDoesNotDelayOtherSongHit(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	otherURL := strings.Replace(videoURL(payload), "1344-", "99-", 1)
	for i, target := range []string{videoURL(payload), otherURL} {
		id := []string{"1344", "99"}[i]
		if _, err := s.PrefetchSong(context.Background(), id, target); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s.cfg.ResolveCurrent = func(ctx context.Context, id string) (string, error) {
		close(entered)
		select {
		case <-release:
			return videoURL("new version"), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	done := make(chan error, 1)
	candidate := parsedVideo(t, s, "new version")
	go func() { done <- s.recordSongVideo(context.Background(), "1344", candidate) }()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("confirmation did not start")
	}
	// The same song must remain serialized, and its waiter must be cancellable.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.recordSongVideo(ctx, "1344", candidate); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("same-song confirmation was not serialized", err)
	}
	ctx, cancelHit := context.WithTimeout(context.Background(), time.Second)
	defer cancelHit()
	if source, err := s.Prefetch(ctx, otherURL); err != nil || source != "HIT" {
		t.Fatal("other song hit blocked by API lookup", source, err)
	}
}

func parsedVideo(t *testing.T, s *Server, body string) video {
	t.Helper()
	v, err := s.parse(httptest.NewRequest("GET", videoURL(body), nil))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCurrentVersionWaitsForOldResponseAndRejectsLateRollback(t *testing.T) {
	const fresh = "new current content"
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(fresh))) {
			io.WriteString(w, fresh)
		} else {
			io.WriteString(w, payload)
		}
	})
	cfg.ResolveCurrent = func(context.Context, string) (string, error) { return videoURL(fresh), nil }
	s.cfg.ResolveCurrent = cfg.ResolveCurrent
	if _, err := s.PrefetchSong(context.Background(), "1344", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := testVideoFile(t, cfg, payload), testVideoFile(t, cfg, fresh)
	w := &pausedUsageWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(w, httptest.NewRequest("GET", videoURL(payload), nil)) }()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		close(w.release)
		t.Fatal("old response did not start")
	}
	_, err := s.PrefetchSong(context.Background(), "1344", videoURL(fresh))
	_, oldErr := os.Stat(oldPath)
	close(w.release)
	<-done
	if err != nil || oldErr != nil {
		t.Fatal("new download deleted active old response", err, oldErr)
	}
	if w.Body.String() != payload {
		t.Fatal("old playback damaged")
	}
	s.wg.Wait()
	expectRetained(t, oldPath, false)
	expectRetained(t, newPath, true)
	// A late old URL can still be served, but cannot become the retained version.
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	s.wg.Wait()
	expectRetained(t, oldPath, false)
	expectRetained(t, newPath, true)
	s.Close()
	// Simulate a crash after switching current but before removing the old bytes.
	writeTestFile(t, oldPath, payload)
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	expectRetained(t, oldPath, false)
	expectRetained(t, newPath, true)
}

func TestFailedOrUnconfirmedReplacementKeepsCurrent(t *testing.T) {
	const fresh = "new current content"
	for _, mode := range []string{"download-failed", "api-offline", "different-resource", "api-other-version"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(payload))) {
					io.WriteString(w, payload)
					return
				}
				if mode == "download-failed" {
					io.WriteString(w, "bad")
					return
				}
				io.WriteString(w, fresh)
			})
			s.cfg.ResolveCurrent = func(context.Context, string) (string, error) {
				switch mode {
				case "api-offline":
					return "", errors.New("offline")
				case "different-resource":
					return strings.Replace(videoURL(fresh), "1344-", "99-", 1), nil
				case "api-other-version":
					return videoURL("third version"), nil
				}
				return videoURL(fresh), nil
			}
			if _, err := s.PrefetchSong(context.Background(), "1344", videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			_, err := s.PrefetchSong(context.Background(), "1344", videoURL(fresh))
			if (err != nil) != (mode == "download-failed") {
				t.Fatal(err)
			}
			s.wg.Wait()
			expectRetained(t, testVideoFile(t, cfg, payload), true)
			expectRetained(t, testVideoFile(t, cfg, fresh), false)
			var key string
			if err := s.usage.db.QueryRow(`SELECT version_key FROM current_videos WHERE song_id='1344'`).Scan(&key); err != nil || key != parsedVideo(t, s, payload).key {
				t.Fatal("current version changed", key, err)
			}
		})
	}
}

func TestOlderDownloadFinishingLastCannotReplaceCurrent(t *testing.T) {
	const fresh = "new current content"
	started, release := make(chan struct{}), make(chan struct{})
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(payload))) {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, payload)
		} else {
			io.WriteString(w, fresh)
		}
	})
	s.cfg.ResolveCurrent = func(context.Context, string) (string, error) { return videoURL(fresh), nil }
	done := make(chan error, 1)
	go func() { _, err := s.PrefetchSong(context.Background(), "1344", videoURL(payload)); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("old download did not start")
	}
	_, err := s.PrefetchSong(context.Background(), "1344", videoURL(fresh))
	close(release)
	oldErr := <-done
	if err != nil || oldErr != nil {
		t.Fatal(err, oldErr)
	}
	s.wg.Wait()
	expectRetained(t, testVideoFile(t, cfg, payload), false)
	expectRetained(t, testVideoFile(t, cfg, fresh), true)
}
