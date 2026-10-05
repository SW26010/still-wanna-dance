package cacheproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func testVideoFile(t *testing.T, cfg Config, body string) string {
	t.Helper()
	v, err := (&Server{cfg: cfg}).parse(httptest.NewRequest("GET", videoURL(body), nil))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.videoFile(v.key)
}

func retainedPath(t *testing.T, s *Server, cfg Config, id string) string {
	t.Helper()
	key := strings.Repeat(id, 32)
	return cfg.videoFile(key)
}

func TestCanonicalPublicationMetadataRestartAndEviction(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if err := s.SyncCatalogNames(context.Background(), "20000101000000", "test", map[string]string{"1344": "My song"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`UPDATE songs SET volume=0.8 WHERE song_id='1344'`); err != nil {
		t.Fatal(err)
	}
	if source, err := s.PrefetchSong(context.Background(), "1344", videoURL(payload)); source != "MISS" || err != nil {
		t.Fatal(source, err)
	}
	files, err := filepath.Glob(filepath.Join(cfg.videosDir(), "*.mp4"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	entries, err := os.ReadDir(cfg.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "verification.sqlite", "verification.sqlite-journal", "videos", "tmp", "stepstash.sqlite", ".lock", "stepstash.sqlite-journal":
		default:
			t.Fatal("unexpected duplicate storage", entry.Name())
		}
	}
	var title, checksum string
	var volume float64
	var size int64
	if err := s.usage.db.QueryRow(`SELECT s.name, s.volume, v.md5, v.byte_size FROM songs s JOIN song_media c USING(song_id) JOIN media v USING(md5)`).Scan(&title, &volume, &checksum, &size); err != nil {
		t.Fatal(err)
	}
	if title != "My song" || volume != 0.8 || len(checksum) != 32 || size != int64(len(payload)) {
		t.Fatal(title, volume, checksum, size)
	}
	s.Close()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if source, err := restarted.PrefetchSong(context.Background(), "1344", videoURL(payload)); source != "HIT" || err != nil {
		t.Fatal(source, err)
	}
	setRetentionLimit(t, restarted, 1)
	restarted.trimCache()
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("video not evicted", err)
	}
	if err := restarted.usage.db.QueryRow(`SELECT name FROM songs WHERE song_id='1344'`).Scan(&title); err != nil || title != "My song" {
		t.Fatal("eviction lost song", title, err)
	}
	var versions int
	if err := restarted.usage.db.QueryRow(`SELECT count(*) FROM media`).Scan(&versions); err != nil || versions != 1 {
		t.Fatal("eviction lost version metadata", versions, err)
	}
}

func TestActiveReaderAndFailedDownloadKeepExistingVersion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if fail {
					io.WriteString(w, "bad")
					return
				}
				io.WriteString(w, payload)
			})
			oldPath := testVideoFile(t, cfg, "old version")
			writeTestFile(t, oldPath, "old version")
			oldVideo, _ := s.parse(httptest.NewRequest("GET", videoURL("old version"), nil))
			s.pinVideo(oldVideo)
			defer s.releaseVideo(oldVideo)
			if err := s.recordVideo(context.Background(), oldVideo); err != nil {
				t.Fatal(err)
			}
			if err := s.recordSongVideo(context.Background(), "1344", oldVideo); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, err = s.PrefetchSong(context.Background(), "1344", videoURL(payload))
			if (err != nil) != fail {
				t.Fatal(err)
			}
			old, err := io.ReadAll(f)
			if err != nil || string(old) != "old version" {
				t.Fatal("opened version changed", string(old), err)
			}
			if fail {
				if _, err := os.Stat(testVideoFile(t, cfg, payload)); !os.IsNotExist(err) {
					t.Fatal("corrupt version published", err)
				}
			} else {
				assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
			}
			assertResponse(t, request(s, "GET", videoURL("old version"), nil), 200, "old version")
		})
	}
}
