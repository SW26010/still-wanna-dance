package cacheproxy

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type exportPauseWriter struct {
	io.Writer
	entered, release chan struct{}
	once             sync.Once
}

type exportNotifyWriter struct {
	io.Writer
	entered chan struct{}
	once    sync.Once
}

func (w *exportNotifyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.Writer.Write(p)
}

func TestExportRequestHistoryLargeZIP(t *testing.T) {
	if os.Getenv("SWD_EXPORT_LARGE_EVIDENCE") == "" {
		t.Skip("set SWD_EXPORT_LARGE_EVIDENCE for 300k-row real ZIP check")
	}
	s, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	const total = 300000
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?)
INSERT INTO request_events(requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,response_bytes,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes)
SELECT x,'GET','bytes=0-','completed',67108864,60000,206,67108864,1000000,1,33554432,33554432,0 FROM n`, total); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "diagnostics.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	z := zip.NewWriter(file)
	entry, err := z.Create("requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := &exportNotifyWriter{Writer: entry, entered: make(chan struct{})}
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		n, err := ExportRequestHistory(t.Context(), s.path, 1, total+2, w)
		if err == nil && n != total {
			t.Errorf("exported %d rows", n)
		}
		done <- err
	}()
	select {
	case <-w.entered:
	case err := <-done:
		t.Fatalf("no ZIP output: %v", err)
	}
	writeStart := time.Now()
	if err := s.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	writeDuration := time.Since(writeStart)
	if err := s.usage.write([]usageEvent{{at: total + 1, method: "GET", outcome: "completed"}}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rows=%d; real ZIP elapsed=%v size=%d; concurrent favorite=%v; concurrent history=ok; Store.Error=%v", total, time.Since(started), info.Size(), writeDuration, s.Error())
	zr, err := zip.NewReader(file, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(r)
	count := 0
	for scanner.Scan() {
		count++
	}
	r.Close()
	if err := scanner.Err(); err != nil || count != total {
		t.Fatal("ZIP rows", count, err)
	}
	for _, reopen := range []bool{false, true} {
		if reopen {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenStore(s.path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
		}
		policies, err := s.SongPolicies(t.Context())
		if err != nil || policies[42] != SongFavorite {
			t.Fatal("favorite", reopen, policies, err)
		}
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM request_events").Scan(&n); err != nil || n != total+1 {
			t.Fatal("history", reopen, n, err)
		}
	}
}

func (w *exportPauseWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.Writer.Write(p)
}

func TestExportRequestHistoryReleasesBusinessSnapshotBeforeZIP(t *testing.T) {
	s, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	if err := s.usage.write([]usageEvent{{at: 1000, method: "GET"}, {at: 1100, method: "HEAD"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	entry, err := z.Create("requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := &exportPauseWriter{Writer: entry, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := ExportRequestHistory(t.Context(), s.path, 1, 5000, w); done <- err }()
	select {
	case <-w.entered:
	case err := <-done:
		t.Fatalf("no export output: %v", err)
	}
	if err := s.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Errorf("favorite while ZIP output paused: %v", err)
	}
	if err := s.usage.write([]usageEvent{{at: 2000, method: "GET"}}); err != nil {
		t.Errorf("history while ZIP output paused: %v", err)
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	decoder := json.NewDecoder(r)
	for _, at := range []float64{1000, 1100} {
		var row map[string]any
		if err := decoder.Decode(&row); err != nil || row["requested_at"] != at {
			t.Fatal(row, err)
		}
	}
	var extra map[string]any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("export snapshot included later write", extra, err)
	}
}

func TestExportRequestHistoryAllMethodsAndBoundaries(t *testing.T) {
	s, cfg := setup(t, nil)
	events := []usageEvent{{at: 999, method: "GET"}, {at: 1000, method: "HEAD", outcome: "canceled"}, {at: 2000, method: "GET"}}
	for i := 0; i < 601; i++ {
		events = append(events, usageEvent{at: 1500, method: "GET", outcome: "failed"})
	}
	if err := s.Store.usage.write(events); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	n, err := ExportRequestHistory(t.Context(), cfg.Store.path, 1000, 2000, &b)
	if err != nil || n != 602 {
		t.Fatal(n, err)
	}
	var first map[string]any
	if err := json.NewDecoder(&b).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first["method"] != "HEAD" || first["outcome"] != "canceled" || first["requested_at"] != float64(1000) {
		t.Fatal(first)
	}
}
