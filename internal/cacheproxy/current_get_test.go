package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCurrentGETResolutionAndFailureLifetime(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			s, _ := setup(t, nil, func(cfg *Config) {
				cfg.ResolvePlayback = func(context.Context, string) (string, error) {
					close(entered)
					<-release
					return "", errors.New("resolution failed")
				}
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "http://api.udon.dance/Api/Songs/play?id=42", nil))
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("resolution did not start")
			}
			snapshot := s.CurrentGETs()
			if method == "GET" {
				if snapshot.Total != 1 || len(snapshot.Requests) != 1 {
					t.Fatal(snapshot)
				}
				r := snapshot.Requests[0]
				if r.SongID != "42" || r.Resource != "" || r.At <= 0 || r.Bytes != 0 || r.ResponseBytes != nil || r.Status != 0 {
					t.Fatal(r)
				}
			} else if snapshot.Total != 0 {
				t.Fatal("HEAD is not GET", snapshot)
			}
			unblock()
			<-done
			if s.CurrentGETs().Total != 0 {
				t.Fatal("completed/failed request retained")
			}
		})
	}
}

type currentGETWriter struct {
	*httptest.ResponseRecorder
	writes  int
	entered chan struct{}
	release chan struct{}
	crash   bool
}

func (w *currentGETWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		close(w.entered)
		<-w.release
		if w.crash {
			panic(http.ErrAbortHandler)
		}
	}
	return w.ResponseRecorder.Write(p)
}

func TestCurrentGETSuccessfulBytesAndAbortCleanup(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "aborted"}[crash], func(t *testing.T) {
			s, cfg := setup(t, nil)
			body := strings.Repeat(payload, 4096)
			writeTestFile(t, cfg.videoFile(videoKey(body)), body)
			w := &currentGETWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{}), crash: crash}
			var once sync.Once
			unblock := func() { once.Do(func() { close(w.release) }) }
			defer unblock()
			r := httptest.NewRequest("GET", videoURL(body), nil)
			r.Header.Set("Range", "bytes=17-90000")
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						t.Errorf("unexpected panic: %v", p)
					}
				}()
				s.ServeHTTP(w, r)
			}()
			select {
			case <-w.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("second write did not start")
			}
			for i := 0; i < 100; i++ {
				snapshot := s.CurrentGETs()
				if snapshot.Total != 1 {
					t.Fatal(snapshot)
				}
				got := snapshot.Requests[0]
				if got.SongID != "" || got.Resource != videoKey(body) || got.Status != 206 || got.ResponseBytes == nil || *got.ResponseBytes != 89984 || got.Bytes <= 0 || got.Bytes >= 89984 {
					t.Fatal(got)
				}
			}
			unblock()
			<-done
			if s.CurrentGETs().Total != 0 {
				t.Fatal("finished request remained active")
			}
		})
	}
}

func TestCurrentGETSnapshotBoundAndStableOrder(t *testing.T) {
	s := &Server{Store: &Store{}}
	var ends []func()
	for i := uint64(1); i <= 75; i++ {
		_, end := s.beginGET(i, time.Now(), strings.Repeat("x", 1000))
		ends = append(ends, end)
	}
	for i := 0; i < 3; i++ {
		r := s.CurrentGETs()
		if r.Total != 75 || len(r.Requests) != 50 || r.Requests[0].ID != 1 || r.Requests[49].ID != 50 || len(r.Requests[0].Range) != 300 {
			t.Fatal(r)
		}
	}
	for _, end := range ends {
		end()
	}
	if s.CurrentGETs().Total != 0 {
		t.Fatal("leaked observations")
	}
}

func BenchmarkCurrentGETSnapshot(b *testing.B) {
	s := &Server{Store: &Store{}}
	for i := uint64(1); i <= 50; i++ {
		g, _ := s.beginGET(i, time.Now(), "bytes=0-999")
		g.identify("42", strings.Repeat("a", 32))
		g.bytes.Store(500)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.CurrentGETs()
	}
}
