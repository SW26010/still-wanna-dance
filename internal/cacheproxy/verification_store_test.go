package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

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
