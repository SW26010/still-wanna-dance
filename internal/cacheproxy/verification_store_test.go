package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScanVerificationSurvivesRestartAndPlayback(t *testing.T) {
	s, v := seedVerifiedTest(t)
	ctx := context.Background()
	cfg := s.cfg
	s.Close()
	for i := range 2 {
		checker, err := NewLocalChecker(cfg.StorageDir, false)
		if err != nil {
			t.Fatal(err)
		}
		result, err := checker.Check(ctx, videoURL(payload))
		checker.Close()
		if err != nil || !result.Hit || result.Reused != (i > 0) {
			t.Fatalf("scan %d: %+v, %v", i, result, err)
		}
	}
	// Age is informational, not a reason to read an unchanged video again.
	store, err := openVerificationStore(cfg.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`UPDATE verified_files SET checked_ns=?`, time.Now().Add(-365*24*time.Hour).UnixNano())
	store.db.Close()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.pinVideo(v)
	defer restarted.releaseVideo(v)
	f, err := restarted.verifiedFile(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if offset, err := f.file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		t.Fatalf("rehashed after restart: %d, %v", offset, err)
	}
}

func TestFullScanDetectsAttributePreservingCorruption(t *testing.T) {
	s, v := seedVerifiedTest(t)
	ctx := context.Background()
	checker, err := NewLocalChecker(s.cfg.StorageDir, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := checker.Check(ctx, videoURL(payload))
	checker.Close()
	if err != nil || !result.Hit {
		t.Fatal(result, err)
	}
	path := s.cfg.videoFile(v.key)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(payload))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, result.Receipt.info.ModTime(), result.Receipt.info.ModTime()); err != nil {
		t.Fatal(err)
	}
	checker, err = NewLocalChecker(s.cfg.StorageDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer checker.Close()
	result, err = checker.Check(ctx, videoURL(payload))
	if err != nil || result.Hit || !result.Corrupt {
		t.Fatal(result, err)
	}
	var records int
	if err := checker.store.db.QueryRow(`SELECT count(*) FROM verified_files`).Scan(&records); err != nil || records != 0 {
		t.Fatal(records, err)
	}
	// A failed full check invalidates the persisted trust for playback too.
	s.pinVideo(v)
	defer s.releaseVideo(v)
	if _, err := s.verifiedFile(ctx, v); !errors.Is(err, errInvalidCache) {
		t.Fatalf("trusted corrupt file: %v", err)
	}
}

func TestConcurrentFullScanChecksSharedVideoOnce(t *testing.T) {
	s, _ := seedVerifiedTest(t)
	checker, err := NewLocalChecker(s.cfg.StorageDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer checker.Close()
	results := make(chan LocalCheckResult, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			result, err := checker.Check(context.Background(), videoURL(payload))
			if err != nil {
				t.Error(err)
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	hashed := 0
	for result := range results {
		if !result.Hit {
			t.Fatal(result)
		}
		if !result.Reused {
			hashed++
		}
	}
	if hashed != 1 {
		t.Fatalf("hashed shared resource %d times", hashed)
	}
}

func TestPublishedFileNeedsNoPlaybackHash(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, err := s.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	s.pinVideo(v)
	defer s.releaseVideo(v)
	f, err := s.verifiedFile(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if offset, err := f.file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		t.Fatalf("publication did not seed verification: %d, %v", offset, err)
	}
}

func TestVerificationDatabaseFailureDoesNotDeleteVideo(t *testing.T) {
	s, v := seedVerifiedTest(t)
	s.verifications.db.Close()
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err == nil {
		t.Fatal("expected database error")
	}
	data, err := os.ReadFile(s.cfg.videoFile(v.key))
	if err != nil || string(data) != payload {
		t.Fatalf("database failure destroyed video: %v", err)
	}
}

func TestWaitingForVerificationCanBeCanceled(t *testing.T) {
	s, v := seedVerifiedTest(t)
	unlock, err := verificationLocks.acquire(context.Background(), s.cfg.StorageDir+v.key)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	checker, err := NewLocalChecker(s.cfg.StorageDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer checker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := checker.Check(ctx, videoURL(payload)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
