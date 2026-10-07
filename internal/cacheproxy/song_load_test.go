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

func TestSongLoadingShortCachedRangeRecoversBeforeBody(t *testing.T) {
	for _, requestedRange := range []string{"", "bytes=-16"} {
		t.Run("range="+requestedRange, func(t *testing.T) {
			body := bytes.Repeat([]byte("range recovery"), int(rangeBlockSize/7))
			failed := make(chan struct{}, 1)
			var queries, freshDownloads atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("stale") == "1" {
					var start, end int64
					fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
					w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
					w.WriteHeader(http.StatusPartialContent)
					w.(http.Flusher).Flush()
					select {
					case failed <- struct{}{}:
					default:
					}
					w.Write(body[start : start+1]) // Legal headers, repeated short body.
					return
				}
				freshDownloads.Add(1)
				http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(body))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))
			old := time.Now().Add(-time.Minute)
			if err := s.ObserveSongURL(ctx, SongURL{SongID: 42, URL: target + "&stale=1", API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: old, ObservedAt: old}); err != nil {
				t.Fatal(err)
			}
			s.cfg.ResolvePlayback = func(ctx context.Context, _, _ string) (string, error) {
				queries.Add(1)
				select {
				case <-failed:
					return target, nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx)
			r.Header.Set("Range", requestedRange)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			status, want := http.StatusOK, body
			if requestedRange != "" {
				status, want = http.StatusPartialContent, body[len(body)-16:]
			}
			if w.Code != status || !bytes.Equal(w.Body.Bytes(), want) || queries.Load() != 1 || freshDownloads.Load() == 0 {
				t.Fatalf("status=%d bytes=%d queries=%d fresh=%d", w.Code, w.Body.Len(), queries.Load(), freshDownloads.Load())
			}
		})
	}
}

func TestSongLoadingFailedCacheDoesNotSwitchAfterBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(rangeBlockSize+16))
	firstBody := make(chan struct{}, 1)
	var freshDownloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stale") != "1" {
			freshDownloads.Add(1)
			http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(body))
			return
		}
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		if start == 0 {
			w.Write(body[:end+1])
			return
		}
		// Do not fail until a client has actually received some body bytes.
		select {
		case <-firstBody:
		case <-r.Context().Done():
			return
		}
		select {
		case firstBody <- struct{}{}:
		default:
		}
		w.Write(body[start : start+1])
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))
	old := time.Now().Add(-time.Minute)
	if err := s.ObserveSongURL(ctx, SongURL{SongID: 42, URL: target + "&stale=1", API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: old, ObservedAt: old}); err != nil {
		t.Fatal(err)
	}
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return target, nil }
	w := &firstBodyRecorder{httptest.NewRecorder(), firstBody}
	var aborted any
	func() {
		defer func() { aborted = recover() }()
		s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
	}()
	if aborted != http.ErrAbortHandler || w.Body.Len() == 0 || w.Body.Len() >= len(body) || freshDownloads.Load() != 0 {
		t.Fatalf("abort=%v bytes=%d new downloads=%d", aborted, w.Body.Len(), freshDownloads.Load())
	}
}

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

func TestFailedCachedURLPreservesRefreshFailure(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	at := time.Now().Add(-time.Minute)
	if err := s.ObserveSongURL(context.Background(), SongURL{SongID: 42, URL: videoURL(payload), API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: at, ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	refreshFailure := errors.New("API refresh unavailable")
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "", refreshFailure }
	_, err := s.PrefetchPlayback(context.Background(), 42)
	if !errors.Is(err, errUpstreamDownload) || !errors.Is(err, refreshFailure) || !strings.Contains(err.Error(), "cached URL refresh") {
		t.Fatalf("lost failure context: %v", err)
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

func TestCachedCandidatesPrecedeBlockedRefresh(t *testing.T) {
	for _, prefetch := range []bool{false, true} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("prefetch=%v/short=%v", prefetch, short), func(t *testing.T) {
				var good atomic.Int32
				s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("bad") != "" {
						if short {
							w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
							w.WriteHeader(200)
							w.(http.Flusher).Flush()
						} else {
							w.WriteHeader(502)
						}
						return
					}
					good.Add(1)
					fmt.Fprint(w, payload)
				})
				s.cfg.ResolvePlayback = func(ctx context.Context, _, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				for i, suffix := range []string{"", "&bad=2", "&bad=1"} {
					at := time.Now().Add(time.Duration(i-10) * time.Minute)
					err := s.ObserveSongURL(ctx, SongURL{SongID: 42, URL: videoURL(payload) + suffix, API: fmt.Sprintf("https://api.udon.dance/source%d", i), Node: []string{"nya", "cf", "cf"}[i], QueryStartedAt: at, ObservedAt: at})
					if err != nil {
						t.Fatal(err)
					}
				}
				if prefetch {
					if _, err := s.PrefetchPlayback(ctx, 42); err != nil {
						t.Fatal(err)
					}
				} else {
					w := httptest.NewRecorder()
					s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
					if w.Code != 200 || w.Body.String() != payload {
						t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
					}
				}
				if good.Load() != 1 {
					t.Fatalf("good downloads=%d", good.Load())
				}
			})
		}
	}
}
