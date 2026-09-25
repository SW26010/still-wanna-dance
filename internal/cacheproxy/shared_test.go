package cacheproxy

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func assertSongResource(t *testing.T, s *Server, id, key string) {
	t.Helper()
	var got string
	if err := s.usage.db.QueryRow(`SELECT version_key FROM current_videos WHERE song_id=?`, id).Scan(&got); err != nil || got != key {
		t.Fatalf("song %s: key=%s want=%s err=%v", id, got, key, err)
	}
}

func TestSharedResourceMappingsSurviveUpdatesAndRestart(t *testing.T) {
	const fresh = "replacement video"
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "99-") {
			io.WriteString(w, fresh)
		} else {
			io.WriteString(w, payload)
		}
	})
	ctx := context.Background()
	shared := videoURL(payload)
	updated := strings.Replace(videoURL(fresh), "1344-", "99-", 1)
	old := parsedVideo(t, s, payload)
	for _, id := range []string{"138", "140"} {
		if _, err := s.PrefetchSong(ctx, id, shared); err != nil {
			t.Fatal(err)
		}
		assertSongResource(t, s, id, old.key)
	}
	files, err := filepath.Glob(filepath.Join(cfg.videosDir(), "*.mp4"))
	if err != nil || len(files) != 1 || filepath.Base(files[0]) != old.key+".mp4" {
		t.Fatal(files, err)
	}
	var songs, versions int
	if err := s.usage.db.QueryRow(`SELECT count(*) FROM songs`).Scan(&songs); err != nil {
		t.Fatal(err)
	}
	if err := s.usage.db.QueryRow(`SELECT count(*) FROM video_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if songs != 2 || versions != 1 {
		t.Fatal("resource URL fabricated a song or duplicated resources", songs, versions)
	}
	cfg.ResolveCurrent = func(_ context.Context, id string) (string, error) {
		if id != "138" && id != "140" {
			t.Errorf("resolved resource number as song: %s", id)
		}
		return updated, nil
	}
	s.cfg.ResolveCurrent = cfg.ResolveCurrent
	if _, err := s.PrefetchSong(ctx, "140", updated); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "138", old.key)
	expectRetained(t, files[0], true)
	s.Close()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	expectRetained(t, files[0], true)
	assertSongResource(t, restarted, "138", old.key)
	// The last song can move away while the old resource remains in use.
	restarted.pinVideo(old)
	if _, err := restarted.PrefetchSong(ctx, "138", updated); err != nil {
		t.Fatal(err)
	}
	expectRetained(t, files[0], true)
	restarted.releaseVideo(old)
	expectRetained(t, files[0], false)
	// Raw playback must neither invent a song nor roll back an explicit mapping.
	assertResponse(t, request(restarted, "GET", shared, nil), 200, payload)
	restarted.wg.Wait()
	expectRetained(t, files[0], false)
	var key string
	if err := restarted.usage.db.QueryRow(`SELECT version_key FROM current_videos WHERE song_id='140'`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, restarted, "138", key)
}

func TestConcurrentSongsShareFlightAndRecordEveryMapping(t *testing.T) {
	var requests atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			io.WriteString(w, payload)
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { _, err := s.PrefetchSong(ctx, "138", videoURL(payload)); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { _, err := s.PrefetchSong(ctx, "140", videoURL(payload)); done <- err }()
	key := parsedVideo(t, s, payload).key
	// Two callers plus the independent worker must pin the same resource.
	for {
		s.retentionMu.Lock()
		pins := s.versionPins[key]
		s.retentionMu.Unlock()
		if pins == 3 {
			break
		}
		if ctx.Err() != nil {
			close(release)
			t.Fatal("second caller did not join flight")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	assertSongResource(t, s, "138", key)
	assertSongResource(t, s, "140", key)
	if requests.Load() != 1 {
		t.Fatal("duplicate download", requests.Load())
	}
	entries, err := os.ReadDir(cfg.videosDir())
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
}
