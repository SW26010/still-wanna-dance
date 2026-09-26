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

func seedVerifiedTest(t *testing.T) (*Server, video) {
	t.Helper()
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected upstream request")
	})
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, err := s.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.videoFile(v.key), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	return s, v
}

func TestVerifiedHandleSharedAcrossResponses(t *testing.T) {
	s, v := seedVerifiedTest(t)
	s.pinVideo(v)
	defer s.releaseVideo(v)
	const count = 12
	results := make(chan *verifiedFile, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			f, err := s.verifiedFile(context.Background(), v)
			if err != nil {
				t.Error(err)
			}
			results <- f
		})
	}
	wg.Wait()
	close(results)
	var shared *verifiedFile
	for f := range results {
		if f == nil {
			t.Fatal("missing verified handle")
		}
		if shared == nil {
			shared = f
		}
		if shared != f {
			t.Fatal("concurrent callers performed separate validations")
		}
	}
	// Both prepare and the handler must reuse this handle. Hashing it a second
	// time at its current EOF would fail, and seeking it would affect the offset.
	for range count {
		wg.Go(func() {
			assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=10-13"}), 206, "abcd")
			assertResponse(t, request(s, "HEAD", videoURL(payload), nil), 200, "")
		})
	}
	wg.Wait()
	if offset, err := shared.file.Seek(0, io.SeekCurrent); err != nil || offset != int64(len(payload)) {
		t.Fatalf("responses changed shared file offset: %d, %v", offset, err)
	}
}

func TestVerifiedHandleReleasedAndNextUseRechecks(t *testing.T) {
	s, v := seedVerifiedTest(t)
	s.pinVideo(v)
	f, err := s.verifiedFile(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	s.releaseVideo(v)
	if _, err := f.file.Stat(); err == nil {
		t.Fatalf("last pin did not close handle: %v", err)
	}
	if err := os.WriteFile(s.cfg.videoFile(v.key), []byte(strings.Repeat("x", len(payload))), 0600); err != nil {
		t.Fatal(err)
	}
	changed := f.info.ModTime().Add(time.Second)
	if err := os.Chtimes(s.cfg.videoFile(v.key), changed, changed); err != nil {
		t.Fatal(err)
	}
	s.pinVideo(v)
	defer s.releaseVideo(v)
	if _, err := s.verifiedFile(context.Background(), v); !errors.Is(err, errInvalidCache) {
		t.Fatalf("subsequent use trusted stale validation: %v", err)
	}
}

func TestVerifiedSequentialRequestsReuseHash(t *testing.T) {
	s, v := seedVerifiedTest(t)
	assertResponse(t, request(s, "HEAD", videoURL(payload), nil), 200, "")
	var checked int64
	if err := s.verifications.db.QueryRow("SELECT checked_ns FROM verified_files").Scan(&checked); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=10-13"}), 206, "abcd")
		assertResponse(t, request(s, "HEAD", videoURL(payload), nil), 200, "")
	}
	s.pinVideo(v)
	defer s.releaseVideo(v)
	f, err := s.verifiedFile(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if f.checkedAt.UnixNano() != checked {
		t.Fatal("sequential requests rehashed unchanged bytes")
	}
	if offset, err := f.file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		t.Fatalf("reopened file was hashed: offset %d, error %v", offset, err)
	}
}

func TestVerifiedIdleInvalidation(t *testing.T) {
	for _, change := range []string{"mtime", "size", "replacement", "missing"} {
		t.Run(change, func(t *testing.T) {
			s, v := seedVerifiedTest(t)
			s.pinVideo(v)
			f, err := s.verifiedFile(context.Background(), v)
			if err != nil {
				t.Fatal(err)
			}
			s.releaseVideo(v)
			path := s.cfg.videoFile(v.key)
			switch change {
			case "mtime":
				changed := f.info.ModTime().Add(time.Second)
				err = os.Chtimes(path, changed, changed)
			case "size":
				err = os.Truncate(path, 1)
			case "replacement":
				// Same size and mtime must not hide a different file identity.
				err = os.WriteFile(path+".new", []byte(strings.Repeat("x", len(payload))), 0600)
				if err == nil {
					err = os.Chtimes(path+".new", f.info.ModTime(), f.info.ModTime())
				}
				if err == nil {
					err = os.Remove(path)
				}
				if err == nil {
					err = os.Rename(path+".new", path)
				}
			case "missing":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			s.pinVideo(v)
			defer s.releaseVideo(v)
			next, err := s.verifiedFile(context.Background(), v)
			switch change {
			case "mtime":
				if err != nil {
					t.Fatal(err)
				}
				if offset, err := next.file.Seek(0, io.SeekCurrent); err != nil || offset != v.size {
					t.Fatalf("changed/expired file was not rehashed: %d, %v", offset, err)
				}
			case "missing":
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing file reused: %v", err)
				}
			default:
				if !errors.Is(err, errInvalidCache) {
					t.Fatalf("changed file reused: %v", err)
				}
			}
		})
	}
}

func TestVerifiedHandleSurvivesPathReplacement(t *testing.T) {
	s, v := seedVerifiedTest(t)
	s.pinVideo(v)
	defer s.releaseVideo(v)
	f, err := s.verifiedFile(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	path := s.cfg.videoFile(v.key)
	if err := os.Rename(path, path+".old"); err == nil {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", len(payload))), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		// Windows protects the pathname by denying rename of an open file.
		t.Logf("OS prevented replacement of active handle: %v", err)
	}
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	if body, err := io.ReadAll(f.reader()); err != nil || string(body) != payload {
		t.Fatalf("verified bytes changed: %q, %v", body, err)
	}
}

func TestVerifiedValidationCapacityAndCancellation(t *testing.T) {
	s, v := seedVerifiedTest(t)
	s.pinVideo(v)
	defer s.releaseVideo(v)
	for range cap(s.localChecks) {
		s.localChecks <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.verifiedFile(ctx, v); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("validation bypassed capacity or ignored cancellation: %v", err)
	}
	for range cap(s.localChecks) {
		<-s.localChecks
	}
	if _, err := s.verifiedFile(context.Background(), v); err != nil {
		t.Fatalf("canceled validation poisoned next caller: %v", err)
	}
}
