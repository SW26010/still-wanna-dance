package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedDownloadUsesLiveLookupScopes(t *testing.T) {
	for _, fillFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("fill_first=%v", fillFirst), func(t *testing.T) {
			started, releaseOrigin := make(chan struct{}), make(chan struct{})
			var oldCalls atomic.Int32
			enter := sync.OnceFunc(func() { close(started) })
			old := videoURL(payload)
			var outside atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("fresh") {
					serveFixtureContent(w, r, payload)
					return
				}
				enter()
				select {
				case <-releaseOrigin:
					if oldCalls.Add(1) > 1 && !fillFirst {
						serveFixtureContent(w, r, payload)
						return
					}
					w.WriteHeader(http.StatusGone)
				case <-r.Context().Done():
				}
			}, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					if id == "1" {
						return old, nil
					}
					outside.Add(1)
					return old + "&fresh=1", nil
				}
			})
			key := videoKey(payload)
			fillTestCatalog(t, s, []CatalogSong{{ID: 1, MD5: key}, {ID: 2, MD5: key}})
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, URL: old, Node: "cf"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			fillDone := make(chan error, 1)
			fill := func() {
				scope := context.WithValue(withExpectedMD5(ctx, key), fillCandidatesKey{}, map[string]struct{}{"1": {}})
				_, err := s.PrefetchDownload(scope, 1)
				fillDone <- err
			}
			getCtx, stopGET := context.WithCancel(ctx)
			defer stopGET()
			resp := httptest.NewRecorder()
			getDone := make(chan struct{})
			get := func() {
				defer close(getDone)
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				s.ServeHTTP(resp, httptest.NewRequestWithContext(getCtx, http.MethodGet, old, nil))
			}
			first, second := get, fill
			if fillFirst {
				first, second = fill, get
			}
			go first()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("first source did not start")
			}
			go second()
			for {
				s.mu.Lock()
				joined := 0
				for d := range s.demands {
					if d.flight != nil && d.key == key {
						joined++
					}
				}
				s.mu.Unlock()
				if joined == 2 {
					break
				}
				select {
				case <-time.After(time.Millisecond):
				case <-ctx.Done():
					t.Fatal("sources did not join")
				}
			}
			if !fillFirst {
				stopGET()
				<-getDone
			}
			close(releaseOrigin)
			err := <-fillDone
			if err != nil {
				t.Fatalf("fill failed after recovery: %v", err)
			}
			if fillFirst {
				<-getDone
				if outside.Load() == 0 || resp.Code != 200 || resp.Body.String() != payload {
					t.Fatalf("GET failed to use its alias: calls=%d status=%d body=%q", outside.Load(), resp.Code, resp.Body.String())
				}
			} else if outside.Load() != 0 {
				t.Fatalf("withdrawn GET queried %d outside aliases", outside.Load())
			}
		})
	}
}

func TestKnownSizeFillSearchesEachAliasOnce(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) }, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(_ context.Context, id string) (string, error) {
			mu.Lock()
			calls[id]++
			mu.Unlock()
			if id == "2" {
				return videoURL(payload), nil
			}
			return videoURL("another version"), nil
		}
	})
	target := seedPreview(t, s, payload)
	fillTestCatalog(t, s, []CatalogSong{{ID: 1, MD5: target.Resource}, {ID: 2, MD5: target.Resource}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := s.PrefetchDownload(withExpectedMD5(ctx, target.Resource), 1)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || calls["1"] != 1 || calls["2"] != 1 {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}

func TestDownloadFillSharesPendingLookupWithPreview(t *testing.T) {
	for _, tc := range []struct {
		fixed, batchFirst bool
	}{
		{true, false}, {true, true}, {false, false}, {false, true},
	} {
		t.Run(fmt.Sprintf("fixed=%v/batch_first=%v", tc.fixed, tc.batchFirst), func(t *testing.T) {
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			defer finish()
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				serveFixtureContent(w, r, payload)
			}, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					calls.Add(1)
					started <- struct{}{}
					select {
					case <-release:
						return videoURL(payload), nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
			})
			target := seedPreview(t, s, payload)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			batchDone := make(chan error, 1)
			previewDone := make(chan *responseRecorder, 1)
			batch := func() {
				batchCtx := ctx
				if tc.fixed {
					batchCtx = withExpectedMD5(ctx, target.Resource)
				}
				_, err := s.PrefetchDownload(batchCtx, 1)
				batchDone <- err
			}
			preview := func() { previewDone <- previewResponse(s, target, nil) }
			first, second := preview, batch
			if tc.batchFirst {
				first, second = batch, preview
			}
			go first()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("first query did not start")
			}
			s.mu.Lock()
			check := s.playbackChecks["1"]
			if check != nil && !tc.batchFirst {
				// Mark the preview-owned check while no caller can cancel it, so
				// the second caller's join becomes observable under this lock.
				check.exclusive = true
			}
			s.mu.Unlock()
			if check == nil {
				t.Fatal("first query was not available for sharing")
			}
			go second()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				s.mu.Lock()
				shared := s.playbackChecks["1"] == check && !check.exclusive
				s.mu.Unlock()
				if shared {
					break
				}
				select {
				case <-started:
					t.Fatal("second caller started a duplicate query")
				case <-ctx.Done():
					t.Fatal("second caller did not join the first query")
				case <-ticker.C:
				}
			}
			finish()
			if err := <-batchDone; err != nil {
				t.Fatalf("batch failed: %v", err)
			}
			response := <-previewDone
			if response.Code != http.StatusOK || response.Body.String() != payload || calls.Load() != 1 {
				t.Fatalf("preview status=%d body=%q API calls=%d", response.Code, response.Body.String(), calls.Load())
			}
		})
	}
}

func TestDownloadFillCancellationKeepsSharedPendingLookup(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixed=%v", fixed), func(t *testing.T) {
			started, canceled := make(chan struct{}, 2), make(chan struct{}, 2)
			release := make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			defer finish()
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) }, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					calls.Add(1)
					started <- struct{}{}
					select {
					case <-release:
						if err := ctx.Err(); err != nil {
							canceled <- struct{}{}
							return "", err
						}
						return videoURL(payload), nil
					case <-ctx.Done():
						canceled <- struct{}{}
						return "", ctx.Err()
					}
				}
			})
			target := seedPreview(t, s, payload)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if fixed {
				ctx = withExpectedMD5(ctx, target.Resource)
			}
			batchDone := make(chan error, 1)
			go func() { _, err := s.PrefetchDownload(ctx, 1); batchDone <- err }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("batch lookup did not start")
			}
			previewDone := make(chan *responseRecorder, 1)
			go func() { previewDone <- previewResponse(s, target, nil) }()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for {
				s.mu.Lock()
				check := s.playbackChecks["1"]
				shared := check != nil && !check.exclusive
				s.mu.Unlock()
				if shared {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("preview did not share the batch lookup")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			if err := <-batchDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("batch error=%v", err)
			}
			finish()
			response := <-previewDone
			select {
			case <-canceled:
				t.Fatal("batch cancellation stopped the shared lookup")
			default:
			}
			if response.Code != http.StatusOK || response.Body.String() != payload || calls.Load() != 1 {
				t.Fatalf("preview status=%d body=%q API calls=%d", response.Code, response.Body.String(), calls.Load())
			}
		})
	}
}

func TestDownloadFillCancellationStopsPendingLookup(t *testing.T) {
	for _, tc := range []struct {
		name               string
		fixed, serverStops bool
	}{
		{"unresolved_song", false, false},
		{"fixed_resource", true, false},
		{"unresolved_song_server_stop", false, true},
		{"fixed_resource_server_stop", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var s *Server
			s, _ = setup(t, nil, func(cfg *Config) {
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					close(started)
					select {
					case <-ctx.Done():
						close(canceled)
						return "", ctx.Err()
					case <-release:
						url := videoURL("new version")
						return url, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: url})
					}
				}
			})
			var target PreviewTarget
			if tc.fixed {
				target = seedPreview(t, s, payload)
			} else if _, err := s.Store.db.Exec("INSERT INTO songs(id) VALUES(1)"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.fixed {
				ctx = withExpectedMD5(ctx, target.Resource)
			}
			done := make(chan error, 1)
			go func() { _, err := s.PrefetchDownload(ctx, 1); done <- err }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("lookup did not start")
			}
			if tc.serverStops {
				s.cancel()
			} else {
				cancel()
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				close(release)
				t.Fatalf("prefetch error = %v", err)
			}
			stopped := false
			select {
			case <-canceled:
				stopped = true
			case <-time.After(250 * time.Millisecond):
			}
			close(release)
			s.wg.Wait()
			var mapped string
			err := s.Store.db.QueryRow("SELECT md5 FROM song_media WHERE song_id=1").Scan(&mapped)
			if !stopped || tc.fixed && (err != nil || mapped != target.Resource) || !tc.fixed && !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("canceled=%t mapped=%q error=%v", stopped, mapped, err)
			}
		})
	}
}

func TestDownloadFillCancellationAndSavedAddressCheck(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		shared, playbackFirst bool
	}{
		{"batch_only", false, false},
		{"batch_then_playback", true, false},
		{"playback_then_batch", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queryStarted, downloadStarted := make(chan struct{}), make(chan struct{})
			queryCanceled, queryFinished := make(chan struct{}), make(chan struct{})
			releaseQuery, releaseDownload := make(chan struct{}), make(chan struct{})
			endQuery := sync.OnceFunc(func() { close(releaseQuery) })
			endDownload := sync.OnceFunc(func() { close(releaseDownload) })
			defer endQuery()
			defer endDownload()
			var downloadOnce sync.Once
			var s *Server
			s, _ = setup(t, func(w http.ResponseWriter, r *http.Request) {
				downloadOnce.Do(func() { close(downloadStarted) })
				select {
				case <-releaseDownload:
					serveFixtureContent(w, r, payload)
				case <-r.Context().Done():
				}
			}, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					close(queryStarted)
					defer close(queryFinished)
					select {
					case <-ctx.Done():
						close(queryCanceled)
						return "", ctx.Err()
					case <-releaseQuery:
						url := videoURL("replacement")
						return url, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: url})
					}
				}
			})
			target := seedPreview(t, s, payload)
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(payload)}); err != nil {
				t.Fatal(err)
			}
			if tc.playbackFirst {
				s.startPlaybackCheck(t.Context(), "1", video{})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.PrefetchDownload(withExpectedMD5(ctx, target.Resource), 1); done <- err }()
			for _, ch := range []<-chan struct{}{queryStarted, downloadStarted} {
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("query or download did not start")
				}
			}
			if tc.shared {
				s.mu.Lock()
				check := s.playbackChecks["1"]
				s.mu.Unlock()
				if check == nil || s.startPlaybackCheck(t.Context(), "1", video{}) != check {
					t.Fatal("playback did not share the in-flight query")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("download result: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("download did not stop")
			}
			if !tc.shared {
				select {
				case <-queryCanceled:
				case <-time.After(250 * time.Millisecond):
					endQuery()
					endDownload()
					<-queryFinished
					t.Fatal("batch-only query continued after cancellation")
				}
			}
			endQuery()
			endDownload()
			select {
			case <-queryFinished:
			case <-time.After(5 * time.Second):
				t.Fatal("query did not finish")
			}
			s.wg.Wait()
			want := target.Resource
			if tc.shared {
				select {
				case <-queryCanceled:
					t.Fatal("batch cancellation stopped the shared playback query")
				default:
				}
				want = parsedVideo(t, s, "replacement").key
			}
			assertSongResource(t, s, "1", want)
		})
	}
}

func TestFixedResourceFillChecksSavedAddress(t *testing.T) {
	for _, scenario := range []string{"updated_before_download", "updated_after_download", "query_failed"} {
		t.Run(scenario, func(t *testing.T) {
			started, resolved, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			startQuery := sync.OnceFunc(func() { close(started) })
			finishQuery := sync.OnceFunc(func() { close(resolved) })
			releaseQuery := sync.OnceFunc(func() { close(release) })
			var queries, replacementDownloads atomic.Int32
			var s *Server
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario == "updated_before_download" {
					select {
					case <-resolved:
					case <-r.Context().Done():
						return
					}
				}
				if r.URL.Query().Get("variant") == "replacement" {
					replacementDownloads.Add(1)
					io.WriteString(w, "replacement")
					return
				}
				serveFixtureContent(w, r, payload)
			}, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					queries.Add(1)
					startQuery()
					defer finishQuery()
					if id != "1" {
						t.Errorf("queried song %s, want 1", id)
					}
					select {
					case <-release:
					case <-ctx.Done():
						return "", ctx.Err()
					}
					if scenario == "query_failed" {
						return "", errors.New("API unavailable")
					}
					url := videoURL("replacement") + "&variant=replacement"
					return url, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: url})
				}
			})
			defer releaseQuery()
			target := seedPreview(t, s, payload)
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(payload)}); err != nil {
				t.Fatal(err)
			}
			if scenario == "updated_before_download" {
				releaseQuery()
			}
			limit, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			ctx, cancel := context.WithCancel(limit)
			defer cancel()
			if source, err := s.PrefetchDownload(withExpectedMD5(ctx, target.Resource), 1); err != nil || source != "MISS" {
				t.Fatal("saved resource must finish independently of the API check", source, err)
			}
			select {
			case <-started:
			case <-limit.Done():
				t.Fatal("download succeeded without checking the song API", queries.Load())
			}
			// Ending the caller must not cancel a check that updates future mappings.
			cancel()
			releaseQuery()
			select {
			case <-resolved:
			case <-limit.Done():
				t.Fatal("background check did not finish")
			}
			s.wg.Wait()
			if queries.Load() != 1 || replacementDownloads.Load() != 0 {
				t.Fatal("duplicated query or switched the pinned download", queries.Load(), replacementDownloads.Load())
			}
			body, err := os.ReadFile(cfg.videoFile(target.Resource))
			if err != nil || string(body) != payload {
				t.Fatal("pinned resource was not published", err)
			}
			want := target.Resource
			if scenario != "query_failed" {
				want = parsedVideo(t, s, "replacement").key
			}
			assertSongResource(t, s, "1", want)
		})
	}
}

func TestFixedResourceFillWithoutSongAssociation(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved_address", true: "local_file"}[local], func(t *testing.T) {
			var queries atomic.Int32
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) }, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(context.Context, string) (string, error) { queries.Add(1); return videoURL("new media"), nil }
			})
			target := seedPreview(t, s, payload)
			if _, err := s.Store.db.Exec("DELETE FROM song_media"); err != nil {
				t.Fatal(err)
			}
			if local {
				if err := os.WriteFile(cfg.videoFile(target.Resource), []byte(payload), 0600); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.Store.db.Exec("INSERT INTO media_urls VALUES(?, 'cf', ?)", target.Resource, videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			result, err := s.NewDownloadFill(t.Context(), false, false).RunResource(target.Resource)
			if err != nil || result.Checked != 1 || result.Failed != 0 || queries.Load() != 0 {
				t.Fatal(result, err, queries.Load())
			}
			got, err := os.ReadFile(cfg.videoFile(target.Resource))
			if err != nil || string(got) != payload {
				t.Fatal("fixed file missing", err)
			}
			var count int
			if err := s.Store.db.QueryRow("SELECT count(*) FROM song_media").Scan(&count); err != nil || count != 0 {
				t.Fatal("invented song mapping", count, err)
			}
		})
	}
}

func TestFixedResourceFillReusesCheckAfterMappingChanges(t *testing.T) {
	resolved := make(chan struct{})
	var freshDownloads, queries atomic.Int32
	var s *Server
	s, _ = setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("address") == "fresh" {
			freshDownloads.Add(1)
			serveFixtureContent(w, r, payload)
			return
		}
		select {
		case <-resolved:
			s.mu.Lock()
			check := s.playbackChecks["1"]
			s.mu.Unlock()
			if check != nil {
				select {
				case <-check.done:
				case <-r.Context().Done():
					return
				}
			}
			http.Error(w, "expired", http.StatusForbidden)
		case <-r.Context().Done():
		}
	}, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			queries.Add(1)
			defer close(resolved)
			url := videoURL(payload) + "&address=fresh"
			return url, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: url})
		}
	})
	target := seedPreview(t, s, payload)
	if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(payload) + "&address=old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL("replacement")}); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", parsedVideo(t, s, "replacement").key)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	source, err := s.PrefetchDownload(withExpectedMD5(ctx, target.Resource), 1)
	if err != nil || source != "MISS" || freshDownloads.Load() == 0 || queries.Load() != 1 {
		t.Fatalf("pinned retry source=%s error=%v fresh downloads=%d queries=%d", source, err, freshDownloads.Load(), queries.Load())
	}
}

func TestFixedResourceFillReusesCompletedCheck(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_resource", true: "changed_resource"}[changed], func(t *testing.T) {
			resolved, release := make(chan struct{}), make(chan struct{})
			finishQuery := sync.OnceFunc(func() { close(resolved) })
			releaseDownload := sync.OnceFunc(func() { close(release) })
			var queries, freshDownloads atomic.Int32
			body := payload
			if changed {
				body = "replacement"
			}
			freshURL := videoURL(body) + "&address=fresh"
			var s *Server
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("address") == "fresh" {
					freshDownloads.Add(1)
					serveFixtureContent(w, r, body)
					return
				}
				select {
				case <-release:
					http.Error(w, "unavailable", http.StatusForbidden)
				case <-r.Context().Done():
				}
			}, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					queries.Add(1)
					defer finishQuery()
					return freshURL, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: freshURL})
				}
			})
			defer releaseDownload()
			target := seedPreview(t, s, payload)
			oldURL := videoURL(payload)
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: oldURL}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := s.PrefetchDownload(withExpectedMD5(ctx, target.Resource), 1)
				result <- err
			}()
			select {
			case <-resolved:
			case <-ctx.Done():
				t.Fatal("saved address did not start its background check")
			}
			// Let the shared lookup retire before failing the old address. The
			// request must retain its result, not rely on an in-flight lookup.
			for {
				s.mu.Lock()
				retired := s.playbackChecks["1"] == nil
				s.mu.Unlock()
				if retired {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("completed lookup did not retire")
				case <-time.After(time.Millisecond):
				}
			}
			releaseDownload()
			if changed {
				// The cached API check belongs to the first pass. A different MD5
				// leaves the original download pending until its caller cancels.
				s.mu.Lock()
				a := &s.flights[target.Resource].addresses
				s.mu.Unlock()
				for {
					a.mu.Lock()
					updated := a.changed
					finishedPass := false
					for _, lookup := range a.lookups {
						finishedPass = finishedPass || lookup.refresh.delay > 0
					}
					a.mu.Unlock()
					if finishedPass {
						break
					}
					select {
					case <-updated:
					case <-ctx.Done():
						t.Fatal("first lookup pass did not finish")
					}
				}
				cancel()
			}
			var err error
			select {
			case err = <-result:
			case <-time.After(2 * time.Second):
				t.Fatal("fixed-resource retry did not finish")
			}
			if changed {
				if !errors.Is(err, context.Canceled) || freshDownloads.Load() != 0 {
					t.Fatal("fixed target followed new mapping", err, freshDownloads.Load())
				}
			} else {
				if err != nil || freshDownloads.Load() == 0 {
					t.Fatal("same-resource fresh address was not used", err, freshDownloads.Load())
				}
				got, err := os.ReadFile(cfg.videoFile(target.Resource))
				if err != nil || string(got) != payload {
					t.Fatal("pinned resource was not published", err)
				}
			}
			if queries.Load() != 1 {
				t.Fatal("retry repeated a completed API check", queries.Load())
			}
			wantURL := freshURL
			if changed {
				wantURL = oldURL
			}
			urls, err := s.Store.ResourceURLs(t.Context(), target.Resource, "cf", 10)
			if err != nil || len(urls) != 1 || urls[0].URL != wantURL {
				t.Fatal("unexpected saved resource address", urls, err)
			}
			assertSongResource(t, s, "1", parsedVideo(t, s, body).key)
		})
	}
}
