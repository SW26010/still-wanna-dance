package cacheproxy

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func seedPreview(t *testing.T, s *Server, body string) PreviewTarget {
	t.Helper()
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	for _, query := range []string{
		fmt.Sprintf("INSERT INTO media(md5,byte_size) VALUES('%s',%d) ON CONFLICT DO NOTHING", key, len(body)),
		"INSERT INTO songs(id) VALUES(1) ON CONFLICT DO NOTHING",
		fmt.Sprintf("INSERT INTO song_media(song_id,md5) VALUES(1,'%s') ON CONFLICT(song_id) DO UPDATE SET md5=excluded.md5", key),
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return PreviewTarget{Resource: key, Size: int64(len(body)), AdmissionSongID: "1"}
}

func previewResponse(s *Server, target PreviewTarget, headers map[string]string) (w *responseRecorder) {
	w = &responseRecorder{ResponseRecorder: httptest.NewRecorder()}
	defer func() {
		if crash := recover(); crash != nil {
			if crash != http.ErrAbortHandler {
				panic(crash)
			}
			w.aborted = true
		}
	}()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/media/preview", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	s.ServePreview(w, r, target)
	return w
}

func assertPreviewUncounted(t *testing.T, s *Server, target PreviewTarget) {
	t.Helper()
	s.usage.flush()
	for _, table := range []string{"request_events", "song_playback", "song_usage"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s: count=%d error=%v", table, count, err)
		}
	}
	var at int64
	if err := s.db.QueryRow("SELECT last_requested_at FROM media_access WHERE md5=?", target.Resource).Scan(&at); err != nil || at <= 0 {
		t.Fatalf("resource access missing: %d %v", at, err)
	}
	if stats := s.TrafficStats(); stats.Hits != 0 || stats.Misses != 0 || stats.SavedBytes != 0 {
		t.Fatalf("preview changed traffic: %+v", stats)
	}
	var total int64
	if err := s.db.QueryRow("SELECT COALESCE(sum(download_bytes+cache_bytes+local_bytes),0) FROM traffic_totals").Scan(&total); err != nil || total != 0 {
		t.Fatalf("response source totals changed: %d %v", total, err)
	}
}

func TestPreviewColdHotAndNoGameEffects(t *testing.T) {
	var upstream, activation, queries atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		http.ServeContent(w, r, "video.mp4", time.Time{}, strings.NewReader(payload))
	}, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.BeginVideoRequest = func(time.Time) func() { activation.Add(1); return func() { activation.Add(1) } }
		cfg.ResolvePlayback = func(context.Context, string) (string, error) { queries.Add(1); return videoURL(payload), nil }
	})
	target := seedPreview(t, s, payload)
	for range 2 {
		assertResponse(t, previewResponse(s, target, map[string]string{"Range": "bytes=2-9"}), 206, payload[2:10])
	}
	assertPreviewUncounted(t, s, target)
	if upstream.Load() == 0 || queries.Load() != 1 || activation.Load() != 0 {
		t.Fatalf("upstream=%d queries=%d activation=%d", upstream.Load(), queries.Load(), activation.Load())
	}
	// Known complete files must be readable without any URL refresh.
	if err := os.WriteFile(cfg.videoFile(target.Resource), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	assertResponse(t, previewResponse(s, target, nil), 200, payload)
	if queries.Load() != 1 {
		t.Fatal("local preview refreshed upstream")
	}
	assertPreviewUncounted(t, s, target)
}

func TestPreviewStaysOnClickTimeResourceAndRechecksSongPolicy(t *testing.T) {
	var queries atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected download") }, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			queries.Add(1)
			return videoURL("new-version"), nil
		}
	})
	target := seedPreview(t, s, payload)
	if err := os.WriteFile(cfg.videoFile(target.Resource), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PreparePreview(context.Background(), cfg.libraryPaths(), DownloadTarget{SongID: "1"})
	if err != nil || prepared != target {
		t.Fatal(prepared, err)
	}
	seedPreview(t, s, "new-version")
	assertResponse(t, previewResponse(s, prepared, nil), 200, payload)
	if err := s.SetSongPolicy(context.Background(), 1, SongBlocked); err != nil {
		t.Fatal(err)
	}
	if w := previewResponse(s, prepared, nil); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	prepared.AdmissionSongID = ""
	assertResponse(t, previewResponse(s, prepared, nil), 200, payload)
	if queries.Load() != 0 {
		t.Fatal("local preview resolved address")
	}
}

func TestPreviewRejectsNewVersionWithoutInvalidatingObservation(t *testing.T) {
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1) }, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			now := time.Now()
			err := cfg.Store.ObserveSongURL(ctx, SongURL{SongID: 1, URL: videoURL("new-version"), API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: now, ObservedAt: now})
			return videoURL("new-version"), err
		}
	})
	target := seedPreview(t, s, payload)
	w := previewResponse(s, target, nil)
	if w.Code < 400 || downloads.Load() != 0 {
		t.Fatal(w.Code, w.Body.String(), downloads.Load())
	}
	urls, err := s.SongURLs(context.Background(), 1, "")
	if err != nil || len(urls) != 1 || urls[0].MD5 == target.Resource {
		t.Fatal("new version observation was rejected", urls, err)
	}
	var mapped string
	if err := s.db.QueryRow("SELECT md5 FROM song_media WHERE song_id=1").Scan(&mapped); err != nil || mapped != urls[0].MD5 {
		t.Fatal("fresh preview API evidence did not update mapping", mapped, err)
	}
}

func TestPreviewMalformedRangeDoesNotResolveOrDownload(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected download") }, func(cfg *Config) {
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			t.Error("unexpected API")
			return videoURL(payload), nil
		}
	})
	target := seedPreview(t, s, payload)
	for _, value := range []string{"bytes=0-1,3-4", "bytes=100-200", "garbage"} {
		if w := previewResponse(s, target, map[string]string{"Range": value}); w.Code != 416 {
			t.Fatal(value, w.Code, w.Body.String())
		}
	}
}

func TestPreviewClockDoesNotAnchorGameQueue(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	target := seedPreview(t, s, payload)
	if err := s.UpdatePrefetchQueue([]int64{1}, true); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	before := s.prefetchSources["1"].startAt
	s.mu.Unlock()
	clock := &playbackClock{at: time.Now(), limit: target.Size, media: estimateMediaTiming(target.Size, 500)}
	ctx := context.WithValue(context.Background(), playbackOriginKey{}, originPreview)
	ctx = context.WithValue(ctx, playbackClockKey{}, clock)
	d, release, err := s.addDemand(ctx, video{key: target.Resource, size: target.Size}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s.mu.Lock()
	end, start := s.queuePlaybackEndLocked(), s.prefetchSources["1"].startAt
	s.mu.Unlock()
	if !end.IsZero() || start.Sub(before) > time.Second || d.clock != clock || d.firstBlock == d.lastBlock {
		t.Fatal("preview changed queue or lost GET timing", end, before, start, d)
	}
}

type previewWriteHook struct {
	*httptest.ResponseRecorder
	once  sync.Once
	after func()
}

func (w *previewWriteHook) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.once.Do(w.after)
	return n, err
}

func TestPreviewReusesBlocksAndRefillsAfterReclamation(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		t.Run(fmt.Sprint("sequential=", sequential), func(t *testing.T) {
			body := strings.Repeat("x", int(3*rangeBlockSize))
			var downloads atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				w.Header().Set("ETag", `"old-validator"`)
				http.ServeContent(w, r, "video.mp4", time.Time{}, strings.NewReader(body))
			}, func(cfg *Config) {
				cfg.CacheUnlimited = false
				cfg.MaxCacheBytes = 0
				cfg.AggressiveCacheReclamation = "off"
				cfg.ResolvePlayback = func(context.Context, string) (string, error) {
					t.Error("cached blocks triggered API")
					return "", fmt.Errorf("offline")
				}
			})
			target := seedPreview(t, s, body)
			r, _ := http.NewRequest("GET", videoURL(body), nil)
			v, _ := s.parseResolved(r)
			status := http.StatusPartialContent
			if sequential {
				status = http.StatusOK
			}
			local, err := s.newLocalResource(v, &http.Response{StatusCode: status, Header: http.Header{"Etag": {`"old-validator"`}}}, s.cfg.Logger)
			if err != nil {
				t.Fatal(err)
			}
			defer local.storage.release()
			for block := int64(0); block < 2; block++ {
				if err := local.storage.store(block, []byte(body[block*rangeBlockSize:(block+1)*rangeBlockSize])); err != nil {
					t.Fatal(err)
				}
			}
			// Unknown catalog size must use the existing resource, without API work.
			if _, err := s.db.Exec("UPDATE media SET byte_size=NULL WHERE md5=?", target.Resource); err != nil {
				t.Fatal(err)
			}
			target.Size = 0
			assertResponse(t, previewResponse(s, target, map[string]string{"Range": "bytes=0-15"}), 206, body[:16])
			if downloads.Load() != 0 {
				t.Fatal("readable Range contacted upstream")
			}
			// Reclamation after serving begins must retain the normal refill path.
			w := &previewWriteHook{ResponseRecorder: httptest.NewRecorder(), after: func() {
				local.storage.removeFiles(local.storage.retire(func(block int64) bool { return block == 1 }))
				s.notifyResourceAvailability(v.key)
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			read := httptest.NewRequest("GET", "http://127.0.0.1/media/preview", nil).WithContext(ctx)
			read.Header.Set("Range", fmt.Sprintf("bytes=0-%d", 2*rangeBlockSize-1))
			s.ServePreview(w, read, target)
			if w.Code != 206 || w.Body.String() != body[:2*rangeBlockSize] || downloads.Load() != 1 {
				t.Fatal(w.Code, w.Body.Len(), downloads.Load())
			}
		})
	}
}

func TestPreviewKnownAddressBeyondAPILookupBudget(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }, func(cfg *Config) {
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			t.Error("known URL ignored")
			return "", fmt.Errorf("offline")
		}
	})
	target := seedPreview(t, s, payload)
	for id := 2; id <= 12; id++ {
		if _, err := s.db.Exec("INSERT INTO songs(id) VALUES(?)", id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("INSERT INTO song_media(song_id,md5) VALUES(?,?)", id, target.Resource); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := s.ObserveSongURL(t.Context(), SongURL{SongID: 12, URL: videoURL(payload), API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: now, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	assertResponse(t, previewResponse(s, target, nil), 200, payload)
}

func TestPreviewAndGameShareFlightWithIndependentIdentityAndCancellation(t *testing.T) {
	for _, previewFirst := range []bool{true, false} {
		t.Run(fmt.Sprint("previewFirst=", previewFirst), func(t *testing.T) {
			entered, gate := make(chan struct{}, 1), make(chan struct{})
			var downloads atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				entered <- struct{}{}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				http.ServeContent(w, r, "video.mp4", time.Time{}, strings.NewReader(payload))
			}, func(cfg *Config) {
				cfg.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(payload), nil }
			})
			target := seedPreview(t, s, payload)
			previewCtx, cancelPreview := context.WithCancel(t.Context())
			defer cancelPreview()
			gameDone, previewDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			run := func(preview bool) {
				go func() {
					w := httptest.NewRecorder()
					if preview {
						s.ServePreview(w, httptest.NewRequest("GET", "http://127.0.0.1/media/preview", nil).WithContext(previewCtx), target)
						previewDone <- w
					} else {
						s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=1", nil))
						gameDone <- w
					}
				}()
			}
			run(previewFirst)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream not started")
			}
			run(!previewFirst)
			deadline := time.Now().Add(5 * time.Second)
			for {
				s.mu.Lock()
				count := len(s.demands)
				s.mu.Unlock()
				if count == 2 {
					break
				}
				if time.Now().After(deadline) {
					close(gate)
					t.Fatal("requests did not share two sources", count)
				}
				time.Sleep(time.Millisecond)
			}
			origins := 0
			for _, get := range s.CurrentGETs().Requests {
				if get.Origin == originPreview {
					origins++
				}
			}
			if origins != 1 {
				t.Error("shared flight lost per-request origin", origins)
			}
			cancelPreview()
			select {
			case <-previewDone:
			case <-time.After(5 * time.Second):
				close(gate)
				t.Fatal("preview did not cancel")
			}
			close(gate)
			select {
			case w := <-gameDone:
				if w.Code != 200 || w.Body.String() != payload {
					t.Fatal(w.Code, w.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("game was canceled with preview")
			}
			s.usage.flush()
			for _, table := range []string{"request_events", "song_usage", "song_playback"} {
				var count int
				if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatal(table, count, err)
				}
			}
			if downloads.Load() != 1 {
				t.Fatal("duplicate download", downloads.Load())
			}
		})
	}
}

func TestPreviewExistingSequentialBlocksBindEarlierReadersTo200Execution(t *testing.T) {
	body := strings.Repeat("s", int(3*rangeBlockSize))
	entered, headers, bodyGate := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	var allowHeaders, allowBody sync.Once
	defer allowHeaders.Do(func() { close(headers) })
	defer allowBody.Do(func() { close(bodyGate) })
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-headers:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-bodyGate:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, body)
	})
	target := seedPreview(t, s, body)
	r, _ := http.NewRequest("GET", videoURL(body), nil)
	v, _ := s.parseResolved(r)
	local, err := s.newLocalResource(v, &http.Response{StatusCode: 200, Header: make(http.Header)}, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer local.storage.release()
	if err := local.storage.store(0, []byte(body[:rangeBlockSize])); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	run := func(ctx context.Context, block int64) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "http://127.0.0.1/media/preview", nil).WithContext(ctx)
			r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", block*rangeBlockSize, (block+1)*rangeBlockSize-1))
			s.ServePreview(w, r, target)
			done <- w
		}()
		return done
	}
	wait := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for read mapping")
			case <-time.After(time.Millisecond):
			}
		}
	}
	first := run(firstCtx, 1)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := run(ctx, 2)
	key1, key2 := downloadTaskKey(v.key, v.size, 1), downloadTaskKey(v.key, v.size, 2)
	wait(func() bool {
		obs := s.scheduler.Observation()
		return obs.Tasks[key1].Readers == 1 && obs.Tasks[key2].Readers == 1
	})
	allowHeaders.Do(func() { close(headers) })
	wait(func() bool {
		obs := s.scheduler.Observation()
		return obs.Tasks[key1].Readers == 2 && obs.Tasks[key2].Readers == 0
	})
	cancelFirst()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait(func() bool {
		obs := s.scheduler.Observation()
		return obs.Tasks[key1].Readers == 1 && !obs.Tasks[key1].Retiring
	})
	allowBody.Do(func() { close(bodyGate) })
	select {
	case w := <-second:
		if w.Code != 206 || w.Body.String() != body[2*rangeBlockSize:] {
			t.Fatal(w.Code, w.Body.Len())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
