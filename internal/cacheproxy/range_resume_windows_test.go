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

func TestPausedCleanupFailureIsLoggedAndRetriedAtStartup(t *testing.T) {
	s, cfg := setup(t, nil)
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parseResolved(r)
	var log bytes.Buffer
	d, err := s.newRangeDownload(v, &http.Response{Header: make(http.Header)}, slog.New(slog.NewTextHandler(&log, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.storage.file.WriteAt([]byte(payload[:4]), 0); err != nil {
		d.storage.release()
		t.Fatal(err)
	}
	d.partial[0] = &rangeResumeBlock{video: v, received: 4, attempts: new(resourceAttempts)}
	s.paused = map[string]*rangeDownload{v.key: d}
	path := d.storage.file.Name()
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
	if len(next.paused) != 0 {
		t.Fatal("new engine recovered a previous engine's progress")
	}
	if _, err := next.verifiedFile(context.Background(), v); !os.IsNotExist(err) {
		t.Fatal("partial content became a cache hit", err)
	}
}
