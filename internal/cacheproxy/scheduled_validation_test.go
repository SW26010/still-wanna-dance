package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// Start at the same boundary as an entirely downloaded, unpublished resource.
func completedRangeFlight(t *testing.T, s *Server, body string) (*flight, video) {
	t.Helper()
	r, _ := http.NewRequest("GET", videoURL(body), nil)
	v, err := s.parseResolved(r)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.newLocalResource(v, &http.Response{Header: make(http.Header)}, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	for block := int64(0); block < blockCount(v.size, rangeBlockSize); block++ {
		start := block * rangeBlockSize
		if err := d.storage.store(block, []byte(body[start:start+blockBytes(v.size, block, rangeBlockSize)])); err != nil {
			t.Fatal(err)
		}
	}
	f := &flight{ranges: d, streaming: make(chan struct{}), log: s.cfg.Logger}
	f.progress = startProgress(f.log, v.size, time.Minute)
	s.pinVideo(v)
	t.Cleanup(func() {
		f.progress.finish(nil)
		if f.spool != nil {
			f.spool.release()
		}
		d.storage.release()
		s.releaseVideo(v)
	})
	return f, v
}

func TestDownloadedValidationSharesCapacityAndCancels(t *testing.T) {
	s, cfg := setup(t, nil)
	f, v := completedRangeFlight(t, s, payload)
	for range cap(s.localChecks) {
		s.localChecks <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := s.publishScheduled(ctx, cfg.videoFile(v.key), v, f)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("final validation bypassed occupied capacity: %v", err)
	}
	if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Fatal("published without a validation slot", err)
	}
	if len(s.localChecks) != cap(s.localChecks) {
		t.Fatal("canceled waiter released someone else's validation slot")
	}
	for range cap(s.localChecks) {
		<-s.localChecks
	}
	// A later flight can validate and publish after the competing work leaves.
	next, v := completedRangeFlight(t, s, payload)
	if err := s.publishScheduled(context.Background(), cfg.videoFile(v.key), v, next); err != nil {
		t.Fatal(err)
	}
	if len(s.localChecks) != 0 {
		t.Fatal("completed validation leaked capacity")
	}
	if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(got) != payload {
		t.Fatal("validated file was not published", err)
	}
}

func TestDownloadedValidationFailureReleasesCapacity(t *testing.T) {
	s, cfg := setup(t, nil)
	f, v := completedRangeFlight(t, s, payload)
	if err := os.WriteFile(localBlockPath(t, f.ranges.storage, 0), []byte("!"+payload[1:]), 0600); err != nil {
		t.Fatal(err)
	}
	err := s.publishScheduled(context.Background(), cfg.videoFile(v.key), v, f)
	if !errors.Is(err, errUpstreamDownload) || len(s.localChecks) != 0 {
		t.Fatalf("failed validation lost its error or leaked capacity: %v slots=%d", err, len(s.localChecks))
	}
	if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Fatal("published corrupt content", err)
	}
}

func TestDownloadedValidationDistinguishesStorageAndIntegrity(t *testing.T) {
	for _, mode := range []string{"read_failure", "checksum_failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := localBlockPath(t, f.ranges.storage, 0)
			switch mode {
			case "read_failure":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "checksum_failure":
				if err := os.WriteFile(path, []byte("!"+payload[1:]), 0600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			err := s.storageError(s.publishBlocks(ctx, cfg.videoFile(v.key), v, f))
			local := mode == "read_failure"
			if errors.Is(err, ErrLocalStorage) != local || (s.StorageError() != nil) != local {
				t.Fatalf("storage classification lost: result=%v storage=%v", err, s.StorageError())
			}
			if errors.Is(err, errUpstreamDownload) != (mode == "checksum_failure") {
				t.Fatal("local read or cancellation marked as upstream failure", err)
			}
			if local {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("underlying read failure lost", err)
				}
				if _, reader, nextErr := s.obtain(context.Background(), v); reader != nil || !errors.Is(nextErr, ErrLocalStorage) {
					t.Fatal("storage failure did not stop next download", nextErr)
				}
			} else if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			if len(s.localChecks) != 0 {
				t.Fatal("validation leaked capacity")
			}
			if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
				t.Fatal("failed validation published a file", err)
			}
		})
	}
}
