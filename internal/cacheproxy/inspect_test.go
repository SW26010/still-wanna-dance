package cacheproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReuseLocalSongRestoresMissingResourceRecord(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("reuse unexpectedly requested upstream")
		http.Error(w, "unexpected download", http.StatusInternalServerError)
	})
	ctx := context.Background()
	target := videoURL(payload)
	v, err := s.parse(httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after publishing bytes but before recording the resource.
	writeTestFile(t, cfg.videoFile(v.key), payload)
	var count int
	if err := s.usage.db.QueryRow(`SELECT count(*) FROM media`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expected unregistered file: count=%d err=%v", count, err)
	}
	hit, receipt, err := CheckLocalReceipt(ctx, cfg.StorageDir, target)
	if err != nil || !hit || receipt == nil {
		t.Fatalf("scan: hit=%v receipt=%v err=%v", hit, receipt, err)
	}
	// Repeated reuse must remain idempotent as well as restore both records.
	for i := 0; i < 2; i++ {
		if reused, err := s.ReuseLocalSong(ctx, "1344", target, receipt); err != nil || !reused {
			t.Fatalf("reuse: reused=%v err=%v", reused, err)
		}
		var checksum, sourcePath string
		var size int64
		if err := s.usage.db.QueryRow(`SELECT v.md5, v.byte_size, v.source_path
 FROM media v JOIN song_media s USING(md5) WHERE s.song_id='1344'`).Scan(&checksum, &size, &sourcePath); err != nil {
			t.Fatal(err)
		}
		if checksum != v.key || size != v.size || sourcePath != v.path {
			t.Fatalf("incorrect restored resource: %s %d %s", checksum, size, sourcePath)
		}
	}
}

func TestCheckLocalNeverPublishesOrDeletes(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "videos")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	target := videoURL(payload)
	parser := &Server{cfg: fixtureConfig()}
	v, err := parser.parse(httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, v.key+".mp4")
	for _, body := range []string{"broken", payload} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		hit, err := CheckLocal(context.Background(), root, target)
		if err != nil || !hit {
			t.Fatal(hit, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != body {
			t.Fatal("scan modified cache", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 2 {
			t.Fatal("scan created unexpected state", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckLocal(ctx, root, target); err != context.Canceled {
		t.Fatal(err)
	}
}
