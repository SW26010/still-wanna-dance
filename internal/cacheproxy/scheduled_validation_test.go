package cacheproxy

import (
	"context"
	"errors"
	"io"
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
	d, err := s.newRangeDownload(v, &http.Response{Header: make(http.Header)}, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.storage.file.WriteString(body); err != nil {
		t.Fatal(err)
	}
	for block := int64(0); block < (v.size+rangeBlockSize-1)/rangeBlockSize; block++ {
		d.finish(block)
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
	if _, err := f.ranges.storage.file.WriteAt([]byte("!"), 0); err != nil {
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

// Trigger only when the final hash reaches its first read, after Seek and Stat.
// The completed fixture has no writers/readers that share this file offset.
type validationReadContext struct {
	context.Context
	file   *os.File
	before func()
}

func (c *validationReadContext) Err() error {
	if offset, err := c.file.Seek(0, io.SeekCurrent); err == nil && offset == 0 && c.before != nil {
		before := c.before
		c.before = nil
		before()
	}
	return c.Context.Err()
}

func TestDownloadedValidationDistinguishesStorageAndIntegrity(t *testing.T) {
	for _, mode := range []string{"read_failure", "seek_failure", "checksum_failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &validationReadContext{Context: parent, file: f.ranges.storage.file}
			switch mode {
			case "read_failure":
				ctx.before = func() {
					if err := ctx.file.Close(); err != nil {
						t.Error(err)
					}
				}
			case "seek_failure":
				if err := ctx.file.Close(); err != nil {
					t.Fatal(err)
				}
			case "checksum_failure":
				if _, err := ctx.file.WriteAt([]byte("!"), 0); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				ctx.before = cancel
			}
			_, _, err := s.prepare(ctx, v, f)
			local := mode == "read_failure" || mode == "seek_failure"
			if errors.Is(err, ErrLocalStorage) != local || (s.StorageError() != nil) != local {
				t.Fatalf("storage classification lost: result=%v storage=%v", err, s.StorageError())
			}
			if errors.Is(err, errUpstreamDownload) != (mode == "checksum_failure") {
				t.Fatal("local read or cancellation marked as upstream failure", err)
			}
			if local {
				if !errors.Is(err, os.ErrClosed) {
					t.Fatal("underlying local error lost", err)
				}
				if _, reader, nextErr := s.obtain(context.Background(), v); reader != nil || !errors.Is(nextErr, ErrLocalStorage) {
					t.Fatal("storage failure did not stop the next cold download", nextErr)
				}
			} else if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			if len(s.localChecks) != 0 {
				t.Fatal("failed validation leaked capacity")
			}
			if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
				t.Fatal("failed validation published a file", err)
			}
		})
	}
}
