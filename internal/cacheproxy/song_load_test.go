package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/vrclog"
)

// The stale address supplies valid range headers, then only one byte.
func writeShortCachedRange(w http.ResponseWriter, r *http.Request, body []byte, headers <-chan struct{}, failed chan<- struct{}) {
	var start, end int64
	fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
	w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
	w.WriteHeader(http.StatusPartialContent)
	w.(http.Flusher).Flush()
	select {
	case <-headers:
	case <-r.Context().Done():
		return
	}
	select {
	case failed <- struct{}{}:
	default:
	}
	w.Write(body[start : start+1])
}

func TestSongLoadingShortCachedRangeRecoversBeforeBody(t *testing.T) {
	for _, tc := range []struct {
		name, requestedRange  string
		owner, changedVersion bool
	}{
		{name: "full"},
		{name: "range", requestedRange: "bytes=-16"},
		{name: "owner range", owner: true, requestedRange: "bytes=-16"},
		{name: "owner changed version", owner: true, changedVersion: true, requestedRange: "bytes=-16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.Repeat([]byte("range recovery"), int(rangeBlockSize/7))
			failed := make(chan struct{}, 1)
			headers := make(chan struct{})
			flushed := sync.OnceFunc(func() { close(headers) })
			if !tc.owner {
				flushed() // Ordinary responses can hold headers until the body is ready.
			}
			var queries, freshDownloads atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("stale") == "1" {
					writeShortCachedRange(w, r, body, headers, failed)
					return
				}
				freshDownloads.Add(1)
				http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(body))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))

			if err := s.ObserveSongURL(ctx, PlaybackObservation{SongID: 42, URL: target + "&stale=1", Node: "cf"}); err != nil {
				t.Fatal(err)
			}
			s.cfg.ResolvePlayback = func(ctx context.Context, _ string) (string, error) {
				queries.Add(1)
				select {
				case <-failed:
					if tc.changedVersion {
						return videoURL("new version"), nil
					}
					return target, nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx)
			r.Header.Set("Range", tc.requestedRange)
			if tc.owner {
				s.playerRole = vrclog.Owner
				r.Header.Set("User-Agent", "NSPlayer/12")
			}
			w := &playerHeaderWriter{httptest.NewRecorder(), flushed}
			var aborted any
			func() {
				defer func() { aborted = recover() }()
				s.ServeHTTP(w, r)
			}()
			if tc.changedVersion {
				if aborted != http.ErrAbortHandler || w.Code != 206 || w.Body.Len() != 0 || freshDownloads.Load() != 0 {
					t.Fatal("incompatible replacement", aborted, w.Code, w.Body.Len(), freshDownloads.Load())
				}
				return
			}
			status, want := http.StatusOK, body
			if tc.requestedRange != "" {
				status, want = http.StatusPartialContent, body[len(body)-16:]
				if got := w.Result().Header.Get("Content-Range"); got != fmt.Sprintf("bytes %d-%d/%d", len(body)-16, len(body)-1, len(body)) {
					t.Fatal(got)
				}
			}
			if aborted != nil || w.Code != status || !bytes.Equal(w.Body.Bytes(), want) || queries.Load() != 1 || freshDownloads.Load() == 0 {
				t.Fatalf("abort=%v status=%d bytes=%d queries=%d fresh=%d", aborted, w.Code, w.Body.Len(), queries.Load(), freshDownloads.Load())
			}
		})
	}
}

func TestOwnerRetryServesPromisedFullBodyAfterPublication(t *testing.T) {
	body := bytes.Repeat([]byte("p"), int(rangeBlockSize+16))
	headers, failed := make(chan struct{}), make(chan struct{}, 1)
	target := videoURL(string(body))
	modified := time.Now().Truncate(time.Second)
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, modified, modified); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{fmt.Sprintf("%x", md5.Sum(body)): true}
	var s *Server
	resolve := func(ctx context.Context, _ string) (string, error) {
		select {
		case <-failed:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		receipt, err := s.ImportVideo(ctx, source, allowed)
		if err != nil || receipt == nil {
			t.Errorf("import receipt=%v err=%v", receipt, err)
			return "", err
		}
		return target, nil
	}
	s, _ = setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stale") != "1" {
			t.Error("retry downloaded the published file")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeShortCachedRange(w, r, body, headers, failed)
	}, func(c *Config) { c.ResolvePlayback = resolve })
	if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 42, URL: target + "&stale=1", Node: "cf"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.playerRole = vrclog.Owner
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx)
	r.Header.Set("User-Agent", "NSPlayer/12")
	r.Header.Set("Range", "bytes=-16")
	r.Header.Set("If-Range", modified.UTC().Format(http.TimeFormat))
	w := &playerHeaderWriter{httptest.NewRecorder(), sync.OnceFunc(func() { close(headers) })}
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("status=%d bytes=%d, want 200 and %d bytes", w.Code, w.Body.Len(), len(body))
	}
}

func TestSongLoadingSwitchesURLAfterFirstBody(t *testing.T) {
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

	if err := s.ObserveSongURL(ctx, PlaybackObservation{SongID: 42, URL: target + "&stale=1", Node: "cf"}); err != nil {
		t.Fatal(err)
	}
	s.cfg.ResolvePlayback = func(context.Context, string) (string, error) { return target, nil }
	w := &firstBodyRecorder{httptest.NewRecorder(), firstBody}
	var aborted any
	func() {
		defer func() { aborted = recover() }()
		s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
	}()
	if aborted != nil || !bytes.Equal(w.Body.Bytes(), body) || freshDownloads.Load() != 1 {
		t.Fatalf("abort=%v bytes=%d new downloads=%d", aborted, w.Body.Len(), freshDownloads.Load())
	}
}

func TestSongLoadingUsesCachedAddressWithoutWaitingForQuery(t *testing.T) {
	for _, queue := range []bool{false, true} {
		t.Run(fmt.Sprint("queue=", queue), func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			target := videoURL(payload)

			if err := s.ObserveSongURL(ctx, PlaybackObservation{SongID: 42, URL: target, Node: "cf"}); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			s.cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
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
				serveFixtureContent(w, r, payload)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			target := videoURL(payload)
			stale := strings.Replace(target, "660524b4ebadb", "old", 1)

			if err := s.ObserveSongURL(ctx, PlaybackObservation{SongID: 42, URL: stale, Node: "cf"}); err != nil {
				t.Fatal(err)
			}
			s.cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
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
			if urls, err := s.Store.SongURLs(ctx, 42, ""); err != nil || len(urls) != 1 || urls[0].URL != stale {
				t.Fatal("failure discarded resource address", urls, err)
			}
		})
	}
}

func TestQueueLocalHitDoesNotWaitForAPI(t *testing.T) {
	started := make(chan struct{})
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) }, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, _ string) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
	})
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
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

func TestFailedCachedURLWaitsUntilCanceled(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })

	if err := s.ObserveSongURL(context.Background(), PlaybackObservation{SongID: 42, URL: videoURL(payload), Node: "cf"}); err != nil {
		t.Fatal(err)
	}
	refreshFailure := errors.New("API refresh unavailable")
	s.cfg.ResolvePlayback = func(context.Context, string) (string, error) { return "", refreshFailure }
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	_, err := s.PrefetchPlayback(ctx, 42)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost failure context: %v", err)
	}
}

func TestQueueCancellationDoesNotCancelJoinedRangePlayback(t *testing.T) {
	body := bytes.Repeat([]byte("q"), int(rangeBlockSize*2))
	prefix, release := make(chan struct{}), make(chan struct{})
	started := sync.OnceFunc(func() { close(prefix) })
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		if start == 0 {
			started()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Write(body[start : end+1])
	})
	defer close(release)
	key := fmt.Sprintf("%x", md5.Sum(body))
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%s&s=%d", key, len(body))
	s.cfg.ResolvePlayback = func(context.Context, string) (string, error) { return target, nil }
	queueCtx, cancelQueue := context.WithCancel(context.Background())
	defer cancelQueue()
	s.UpdatePrefetchQueue([]int64{42}, false)
	done := make(chan error, 1)
	go func() { _, err := s.PrefetchQueuePlayback(queueCtx, 42); done <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-prefix:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.mu.Lock()
	queuedFlight := s.flights[key]
	s.mu.Unlock()
	if queuedFlight == nil {
		t.Fatal("missing queue flight")
	}
	s.UpdatePrefetchQueue(nil, true)
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
	// The tail Seek can yield and retry block 0 within the same flight. Count
	// shared tasks directly instead of treating every HTTP retry as a new one.
	s.mu.Lock()
	joinedFlight := s.flights[key]
	s.mu.Unlock()
	if joinedFlight != queuedFlight {
		t.Fatal("playback did not share queue flight")
	}
}

func TestCachedCandidatesOnlyUseCFEntry(t *testing.T) {
	for _, entry := range []string{"prefetch", "playback", "preview"} {
		t.Run(entry, func(t *testing.T) {
			var forbidden atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("node") == "nya" {
					forbidden.Add(1)
					serveFixtureContent(w, r, payload)
					return
				}
				w.WriteHeader(502)
			})
			refreshErr := errors.New("API unavailable")
			s.cfg.ResolvePlayback = func(_ context.Context, _ string) (string, error) {
				return "", refreshErr
			}
			for _, node := range []string{"cf", "nya"} {

				if err := s.ObserveSongURL(context.Background(), PlaybackObservation{SongID: 42, URL: videoURL(payload) + "&node=" + node, Node: node}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
			defer cancel()
			switch entry {
			case "prefetch":
				if _, err := s.PrefetchPlayback(ctx, 42); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "playback":
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42&node=nya", nil).WithContext(ctx))
				if w.Code < 400 || ctx.Err() == nil {
					t.Fatal(w.Code, w.Body.String())
				}
			case "preview":
				target := PreviewTarget{Resource: fmt.Sprintf("%x", md5.Sum([]byte(payload))), Size: int64(len(payload)), AdmissionSongID: "42"}
				w := previewResponseContext(ctx, s, target, nil)
				if w.Code < 400 || ctx.Err() == nil {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			if forbidden.Load() != 0 {
				t.Fatal("used a cached URL from another entry")
			}
		})
	}
}
