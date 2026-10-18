package cacheproxy

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestBlockCleanupFailureIsLoggedAndRetriedAtStartup(t *testing.T) {
	s, cfg := setup(t, nil)
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parseResolved(r)
	var log bytes.Buffer
	d, err := s.newLocalResource(v, &http.Response{Header: make(http.Header)}, slog.New(slog.NewTextHandler(&log, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.storage.store(0, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	path := localBlockPath(t, d.storage, 0)
	d.storage.release() // Only the registry remains.
	// Windows denies deletion while this unrelated handle remains open.
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "temporary_cleanup_failed") {
		t.Fatal("cleanup failure was silent", log.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed cleanup did not leave the managed file for retry", err)
	}
	held.Close()
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	assertNoPartial(t, cfg.tempDir())
	if len(next.localResources) != 0 {
		t.Fatal("new engine recovered a previous engine's progress")
	}
	if _, err := next.verifiedFile(context.Background(), v); !os.IsNotExist(err) {
		t.Fatal("partial content became a cache hit", err)
	}
}
