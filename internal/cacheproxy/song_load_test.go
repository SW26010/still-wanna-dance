package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSongLoadingUsesCachedAddressWithoutWaitingForQuery(t *testing.T) {
	for _, queue := range []bool{false, true} {
		t.Run(fmt.Sprint("queue=", queue), func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			target := videoURL(payload)
			old := time.Now().Add(-time.Minute)
			if err := s.ObserveSongURL(ctx, SongURL{SongID: 42, URL: target, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: old, ObservedAt: old}); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			s.cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
				close(started)
				select {
				case <-release:
					return target, nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if queue {
				if source, err := s.PrefetchPlayback(ctx, 42); err != nil || source != "MISS" {
					t.Fatal(source, err)
				}
			} else {
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
				if w.Code != 200 || w.Body.String() != payload {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("query never started")
			}
			if ctx.Err() != nil {
				t.Fatal("cached address waited for query", ctx.Err())
			}
		})
	}
}

func TestSongLoadingFailedCacheReusesPendingQuery(t *testing.T) {
	for _, queue := range []bool{false, true} {
		t.Run(fmt.Sprint("queue=", queue), func(t *testing.T) {
			failed := make(chan struct{})
			var downloads, queries atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				if strings.Contains(r.URL.Path, "old") {
					close(failed)
					w.WriteHeader(403)
					return
				}
				io.WriteString(w, payload)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			target := videoURL(payload)
			stale := strings.Replace(target, "660524b4ebadb", "old", 1)
			old := time.Now().Add(-time.Minute)
			if err := s.ObserveSongURL(ctx, SongURL{SongID: 42, URL: stale, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: old, ObservedAt: old}); err != nil {
				t.Fatal(err)
			}
			s.cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
				queries.Add(1)
				select {
				case <-failed:
					return target, nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if queue {
				if source, err := s.PrefetchPlayback(ctx, 42); err != nil || source != "MISS" {
					t.Fatal(source, err)
				}
			} else {
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
				if w.Code != 200 || w.Body.String() != payload {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			if queries.Load() != 1 || downloads.Load() != 2 {
				t.Fatal("duplicated lookup/download", queries.Load(), downloads.Load())
			}
			if urls, err := s.SongURLs(ctx, 42, ""); err != nil || len(urls) != 0 {
				t.Fatal("failed observation remained usable", urls, err)
			}
		})
	}
}

func TestQueueLocalHitDoesNotWaitForAPI(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.cfg.ResolvePlayback = func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if source, err := s.PrefetchPlayback(ctx, 42); err != nil || source != "HIT" {
		t.Fatal(source, err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("missing background refresh")
	}
}

func TestQueueCancellationDoesNotCancelJoinedRangePlayback(t *testing.T) {
	body := bytes.Repeat([]byte("q"), int(rangeBlockSize*2))
	prefix, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		if start == 0 {
			close(prefix)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Write(body[start : end+1])
	})
	defer close(release)
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return target, nil }
	queueCtx, cancelQueue := context.WithCancel(context.Background())
	defer cancelQueue()
	done := make(chan error, 1)
	go func() { _, err := s.PrefetchPlayback(queueCtx, 42); done <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-prefix:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelQueue()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx)
	r.Header.Set("Range", "bytes=-16")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), body[len(body)-16:]) {
		t.Fatal(w.Code, w.Body.String())
	}
	if requests.Load() != 2 {
		t.Fatal("playback did not share queue flight", requests.Load())
	}
}
