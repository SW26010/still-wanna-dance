package console

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestNetworkCalibrationReusesMetadataPriorityAndPersistence(t *testing.T) {
	c := testConsole(t)
	ctx := t.Context()
	if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261031000000", Songs: []cacheproxy.CatalogSong{{ID: 1}, {ID: 2}, {ID: 3}}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", c.settings.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, score := range map[int]float64{1: 100, 2: 50, 3: 10} {
		if _, err := db.Exec("INSERT INTO song_usage VALUES (?,1,?,?)", id, score, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// Pure expiry does not outrank missing metadata. Song 2 has an obsolete
	// mapping and a failed URL; song 3 has no mapping at all.
	old := time.Now().Add(-25 * time.Hour)
	for _, id := range []int64{1, 2} {
		observation := cacheproxy.SongURL{SongID: id, API: c.apiBase + "/Api/Songs/play", Node: "cf",
			URL: "https://play.udon.dance/files/1/42-old.mp4?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=1234", QueryStartedAt: old, ObservedAt: old}
		if err := c.store.ObserveSongURL(ctx, observation); err != nil {
			t.Fatal(err)
		}
		if id == 2 {
			if err := c.store.RejectSongURL(ctx, observation); err != nil {
				t.Fatal(err)
			}
		}
	}
	candidates := c.metadataSongCandidates(ctx)
	if len(candidates) != 2 || candidates[0] != 2 || candidates[1] != 3 {
		t.Fatalf("metadata repair priority was lost: %v", candidates)
	}
	var playback atomic.Int32
	c.client.CloseIdleConnections()
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: 128 << 20,
				Body: io.NopCloser(io.LimitReader(calibrationZeroReader{}, 128<<20))}, nil
		}
		if r.URL.Path != "/Api/Songs/play" {
			t.Error("calibration requested a catalog")
			return nil, io.EOF
		}
		playback.Add(1)
		var id int64
		fmt.Sscan(r.URL.Query().Get("id"), &id)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=134217728", id, id)}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	// A partial reserve still contributes valid business observations. The next
	// run must reuse them without querying playback again.
	for run := range 2 {
		if err := c.controlNetworkCalibration(false); err != nil {
			t.Fatal(err)
		}
		wait, cancel := context.WithTimeout(ctx, 3*time.Second)
		for {
			state, changed := c.networkDiagnostics.calibration.Snapshot()
			if !state.Running {
				if state.PreparedVideos != 3 || playback.Load() != 3 || state.Reason != "samples_exhausted" || state.Bytes != 384<<20 {
					t.Fatalf("run %d: prepared=%d requests=%d reason=%s", run, state.PreparedVideos, playback.Load(), state.Reason)
				}
				break
			}
			select {
			case <-changed:
			case <-wait.Done():
				t.Fatal(wait.Err())
			}
		}
		cancel()
	}
	for id := int64(1); id <= 3; id++ {
		urls, err := c.store.SongURLs(ctx, id, "cf")
		if err != nil || len(urls) != 1 {
			t.Fatalf("song %d: URL observation was not reused: count=%d err=%v", id, len(urls), err)
		}
		var md5 string
		var size int64
		if err := db.QueryRow("SELECT md5,byte_size FROM song_media JOIN media USING(md5) WHERE song_id=?", id).Scan(&md5, &size); err != nil || md5 != fmt.Sprintf("%032x", id) || size != 128<<20 {
			t.Fatalf("song %d: metadata not updated: md5=%s size=%d err=%v", id, md5, size, err)
		}
	}
	if missing := c.metadataSongCandidates(ctx); len(missing) != 0 {
		t.Fatal("calibration did not advance shared metadata repair", missing)
	}
	var count int
	var score float64
	if err := db.QueryRow("SELECT sum(demand_count),sum(demand_score) FROM song_usage").Scan(&count, &score); err != nil || count != 3 || score != 160 || c.service != nil {
		t.Fatalf("metadata repair changed playback usage or started the engine: count=%d score=%f err=%v", count, score, err)
	}
}

func TestNetworkCalibrationUsesLocalSongsWithoutCatalogRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached int
		usable int32
		age    time.Duration
		node   string
		failed bool
	}{
		{name: "saved_urls_sufficient", cached: 16, usable: 16, node: "cf"},
		{name: "saved_urls_partial", cached: 1, usable: 1, node: "cf"},
		{name: "expired_urls", cached: 16, age: 25 * time.Hour, node: "cf"},
		{name: "failed_urls", cached: 16, node: "cf", failed: true},
		{name: "ineligible_node", cached: 16, node: "nya"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			var catalogs, playback atomic.Int32
			media := make(chan struct{}, 1)
			c.client.CloseIdleConnections()
			c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/Api/Songs/play" {
					playback.Add(1)
					var id int64
					fmt.Sscan(r.URL.Query().Get("id"), &id)
					return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{
						"Location": {fmt.Sprintf("http://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=134217728", id, id)},
					}}, nil
				}
				if r.URL.Host == "play.udon.dance" {
					if r.URL.Scheme != "http" {
						t.Error("saved URL bypassed the resource protocol policy")
					}
					select {
					case media <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				catalogs.Add(1)
				return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			c.mu.Lock()
			calibration, err := c.networkCalibrationLocked()
			c.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			// Opening the result stream can precede catalog updates. Selection must read
			// the current database when the user starts a run, not at construction.
			catalog := cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000"}
			for id := int64(1); id <= 15; id++ {
				catalog.Songs = append(catalog.Songs, cacheproxy.CatalogSong{ID: id})
			}
			if err := c.store.SyncCatalog(t.Context(), catalog); err != nil {
				t.Fatal(err)
			}
			// Accumulated songs remain useful candidates even after a newer list omits
			// them; a fresh playback response decides whether their media still works.
			catalog.Revision, catalog.Songs = "20261031000000", catalog.Songs[14:]
			if err := c.store.SyncCatalog(t.Context(), catalog); err != nil {
				t.Fatal(err)
			}
			// Playback-only identities and songs with complete metadata are candidates
			// too; neither current membership nor metadata repair progress defines them.
			at := time.Now().Add(-time.Minute - tc.age)
			for i := 0; i < tc.cached; i++ {
				id := 16 - i
				observation := cacheproxy.SongURL{
					SongID: int64(id), API: c.apiBase + "/Api/Songs/play", Node: tc.node,
					URL:            fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=134217728&saved=1", id, id),
					QueryStartedAt: at, ObservedAt: at.Add(time.Second),
				}
				if err := c.store.ObserveSongURL(t.Context(), observation); err != nil {
					t.Fatal(err)
				}
				if tc.failed {
					if err := c.store.RejectSongURL(t.Context(), observation); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := c.store.CatalogStatus(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := c.controlNetworkCalibration(false); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
		waiting:
			for {
				state, changed := calibration.Snapshot()
				if !state.Running {
					t.Fatalf("local songs did not reach download: phase=%s reason=%s prepared=%d catalogs=%d", state.Phase, state.Reason, state.PreparedVideos, catalogs.Load())
				}
				select {
				case <-media:
					break waiting
				case <-changed:
				case <-ctx.Done():
					t.Fatal("local preparation did not start network media download")
				}
			}
			calibration.Cancel()
			calibration.Close()
			state, _ := calibration.Snapshot()
			after, err := c.store.CatalogStatus(t.Context())
			if err != nil || catalogs.Load() != 0 || playback.Load() != 16-tc.usable || state.PreparedVideos != 16 || state.PreparedBytes != 2<<30 || c.service != nil || before != after {
				t.Fatalf("local preparation changed its scope: catalogs=%d playback=%d prepared=%d bytes=%d engine=%t catalog_changed=%t err=%v", catalogs.Load(), playback.Load(), state.PreparedVideos, state.PreparedBytes, c.service != nil, before != after, err)
			}
		})
	}
}

func TestNetworkCalibrationAvailableLocalSamplesDoNotSyncCatalog(t *testing.T) {
	for _, source := range []string{"empty", "known_song", "saved_url"} {
		t.Run(source, func(t *testing.T) {
			c := testConsole(t)
			const target = "http://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=134217728"
			if source != "empty" {
				seedCalibrationSong(t, c)
			}
			if source == "saved_url" {
				at := time.Now().Add(-time.Minute)
				if err := c.store.ObserveSongURL(t.Context(), cacheproxy.SongURL{SongID: 42, URL: target,
					API: c.apiBase + "/Api/Songs/play", Node: "cf", QueryStartedAt: at, ObservedAt: at}); err != nil {
					t.Fatal(err)
				}
			}
			var playback, media, unexpected atomic.Int32
			c.client.CloseIdleConnections()
			c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasPrefix(r.URL.Path, "/files/") {
					media.Add(1)
					return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if r.URL.Path != "/Api/Songs/play" {
					unexpected.Add(1)
					return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				playback.Add(1)
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			before, err := c.store.CatalogStatus(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := c.controlNetworkCalibration(false); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			for {
				state, changed := c.networkDiagnostics.calibration.Snapshot()
				if !state.Running {
					wantPrepared, wantPlayback := 1, int32(0)
					wantReason, wantMedia := "network_error", int32(1)
					if source == "empty" {
						wantPrepared = 0
						wantReason, wantMedia = "preparation_insufficient", 0
					} else if source == "known_song" {
						wantPlayback = 1
					}
					after, err := c.store.CatalogStatus(t.Context())
					if err != nil || state.Phase != "failed" || state.Reason != wantReason || state.PreparedVideos != wantPrepared || state.Bytes != 0 || playback.Load() != wantPlayback || media.Load() != wantMedia || unexpected.Load() != 0 || before != after || c.service != nil {
						t.Fatalf("insufficient local candidates changed scope: phase=%s prepared=%d playback=%d unexpected=%d catalog_changed=%t err=%v", state.Phase, state.PreparedVideos, playback.Load(), unexpected.Load(), before != after, err)
					}
					break
				}
				select {
				case <-changed:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

type calibrationZeroReader struct{}

func (calibrationZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
