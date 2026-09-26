package cacheproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertPlaybackSong(t *testing.T, s *Server, id, body string) {
	t.Helper()
	v := parsedVideo(t, s, body)
	var key string
	if err := s.usage.db.QueryRow(`SELECT version_key FROM current_videos WHERE song_id=?`, id).Scan(&key); err != nil || key != v.key {
		t.Fatalf("song %s: key=%s error=%v", id, key, err)
	}
	var count int
	if err := s.usage.db.QueryRow(`SELECT COUNT(*) FROM song_videos WHERE song_id=? AND version_key=?`, id, v.key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing song resource reference: %d %v", count, err)
	}
}

func TestPlaybackSongRecordsColdAndWarmVersions(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(strconv.FormatBool(warm), func(t *testing.T) {
			var body atomic.Value
			body.Store(payload)
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body.Load().(string)) })
			s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(body.Load().(string)), nil }
			s.cfg.ResolveCurrent = func(context.Context, string) (string, error) { return videoURL(body.Load().(string)), nil }
			if warm {
				if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
					t.Fatal(err)
				}
			}
			target := "http://api.udon.dance/Api/Songs/play?id=42"
			assertResponse(t, request(s, "GET", target, nil), 200, payload)
			assertPlaybackSong(t, s, "42", payload)
			updated := "updated song video"
			body.Store(updated)
			assertResponse(t, request(s, "GET", target, nil), 200, updated)
			s.wg.Wait()
			assertPlaybackSong(t, s, "42", updated)
			if _, err := os.Stat(testVideoFile(t, cfg, payload)); !os.IsNotExist(err) {
				t.Fatal("superseded file retained", err)
			}
		})
	}
}

func TestPlaybackSongPersistsAfterEarlyResponsesAndDisconnect(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 4096)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		io.WriteString(w, body[:2048])
		w.(http.Flusher).Flush()
		select {
		case <-release:
			io.WriteString(w, body[2048:])
		case <-r.Context().Done():
		}
	})
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(body), nil }
	local := httptest.NewServer(s)
	defer local.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	for _, test := range []struct{ id, method, rangeValue string }{{"42", "HEAD", ""}, {"43", "GET", "bytes=0-31"}, {"44", "GET", ""}} {
		req, _ := http.NewRequest(test.method, local.URL+"/Api/Songs/play?id="+test.id, nil)
		req.Host = "api.udon.dance"
		req.Header.Set("Range", test.rangeValue)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if test.id == "44" {
			_, err = io.CopyN(io.Discard, resp.Body, 32)
		} else {
			_, err = io.Copy(io.Discard, resp.Body)
		}
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 && resp.StatusCode != 206 {
			t.Fatal(resp.StatusCode)
		}
	}
	var count int
	if err := s.usage.db.QueryRow(`SELECT COUNT(*) FROM current_videos`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("recorded before verification: %d %v", count, err)
	}
	unblock()
	s.wg.Wait()
	for _, id := range []string{"42", "43", "44"} {
		assertPlaybackSong(t, s, id, body)
	}
	if downloads.Load() != 1 {
		t.Fatal("duplicate download", downloads.Load())
	}
}

func TestPlaybackSongFailedValidationPreservesCurrent(t *testing.T) {
	var corrupt atomic.Bool
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if corrupt.Load() {
			io.WriteString(w, strings.Repeat("x", len(payload)))
		} else {
			io.WriteString(w, payload)
		}
	})
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	corrupt.Store(true)
	updated := strings.Repeat("y", len(payload))
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(updated), nil }
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
	s.wg.Wait()
	if !w.aborted && w.Code == 200 {
		t.Fatal("corrupt download succeeded")
	}
	assertPlaybackSong(t, s, "42", payload)
	var count int
	if err := s.usage.db.QueryRow(`SELECT COUNT(*) FROM song_videos`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("corrupt video referenced: %d %v", count, err)
	}
}

func TestPlaybackSongUpdateRetainsOtherSongResource(t *testing.T) {
	var body atomic.Value
	body.Store(payload)
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body.Load().(string)) })
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(body.Load().(string)), nil }
	s.cfg.ResolveCurrent = func(context.Context, string) (string, error) { return videoURL(body.Load().(string)), nil }
	for _, id := range []string{"42", "43"} {
		assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id="+id, nil), 200, payload)
	}
	updated := "new shared version"
	body.Store(updated)
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil), 200, updated)
	s.wg.Wait()
	assertPlaybackSong(t, s, "42", updated)
	assertPlaybackSong(t, s, "43", payload)
	if _, err := os.Stat(testVideoFile(t, cfg, payload)); err != nil {
		t.Fatal("deleted other song's current resource", err)
	}
}

func TestPlaybackSongRecordsCapacityLimitedLocalHit(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	// Fill all worker slots to exercise the independent local validation path.
	for i := 0; i < cap(s.slots); i++ {
		s.slots <- struct{}{}
	}
	defer func() {
		for len(s.slots) > 0 {
			<-s.slots
		}
	}()
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(payload), nil }
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil), 200, payload)
	assertPlaybackSong(t, s, "42", payload)
}
