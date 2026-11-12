package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	// Preparation and responses reuse the handle without reading or seeking it;
	// each response keeps its own SectionReader offset.
	for range count {
		wg.Go(func() {
			assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=10-13"}), 206, "abcd")
			assertResponse(t, request(s, "HEAD", videoURL(payload), nil), 200, "")
		})
	}
	wg.Wait()
	if offset, err := shared.file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
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
	if _, err := s.verifiedFile(context.Background(), v); err != nil {
		t.Fatalf("subsequent use trusted stale validation: %v", err)
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

func TestVideoReadsDoNotWaitForPublicationCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, method, rangeValue, body string
		local                          bool
		status                         int
	}{
		{"local GET", http.MethodGet, "", payload, true, http.StatusOK},
		{"local Range", http.MethodGet, "bytes=10-13", "abcd", true, http.StatusPartialContent},
		{"local HEAD", http.MethodHead, "", "", true, http.StatusOK},
		{"cold GET", http.MethodGet, "", payload, false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.local {
					t.Error("local hit made an upstream request")
				}
				serveFixtureContent(w, r, payload)
			})
			v := parsedVideo(t, s, payload)
			if tc.local {
				if err := os.WriteFile(cfg.videoFile(v.key), []byte(payload), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Hold every whole-file publication slot before the first local open
			// (including the missing-file check that precedes a cold download).
			for range cap(s.localChecks) {
				s.localChecks <- struct{}{}
			}
			release := sync.OnceFunc(func() {
				for range cap(s.localChecks) {
					<-s.localChecks
				}
			})
			t.Cleanup(release)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := httptest.NewRequest(tc.method, videoURL(payload), nil).WithContext(ctx)
			if tc.rangeValue != "" {
				r.Header.Set("Range", tc.rangeValue)
			}
			out := httptest.NewRecorder()
			s.ServeHTTP(out, r)
			if ctx.Err() != nil || out.Code != tc.status || out.Body.String() != tc.body || out.Header().Get("Content-Length") == "" {
				t.Fatalf("read waited for publication capacity: context=%v status=%d headers=%v body=%q", ctx.Err(), out.Code, out.Header(), out.Body.String())
			}
			if len(s.localChecks) != cap(s.localChecks) {
				t.Fatal("read consumed or released a publication slot")
			}
			if !tc.local {
				if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
					t.Fatal("publication bypassed occupied capacity", err)
				}
				release()
				s.wg.Wait()
				if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(got) != payload {
					t.Fatal("publication did not resume after capacity was released", err)
				}
			}
		})
	}
}

func TestPublishedFileNeedsNoPlaybackHash(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
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
		t.Fatalf("playback unexpectedly read published bytes: %d, %v", offset, err)
	}
}

func TestVerifiedFileCancellationDoesNotPoisonNextCaller(t *testing.T) {
	s, v := seedVerifiedTest(t)
	s.pinVideo(v)
	defer s.releaseVideo(v)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.verifiedFile(ctx, v); !errors.Is(err, context.Canceled) {
		t.Fatalf("file open ignored cancellation: %v", err)
	}
	if _, err := s.verifiedFile(context.Background(), v); err != nil {
		t.Fatalf("canceled file open poisoned next caller: %v", err)
	}
}
