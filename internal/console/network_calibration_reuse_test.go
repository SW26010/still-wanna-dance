package console

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestNetworkCalibrationReusesConnectivityPlaybackAccess(t *testing.T) {
	c := testConsole(t)
	seedCalibrationSong(t, c)
	failing := catalogTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("ordinary path unavailable") })
	healthy := catalogTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"http://play.udon.dance/files/1/42-video.mp4?e=0000000000000000000000000000002a&s=134217728"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	c.client.CloseIdleConnections()
	c.client = &http.Client{Transport: &coldBusinessChannels{RoundTripper: failing, channels: []upstreamrequest.Candidate{{ID: "direct/fixture/127.0.0.1", Mode: "direct", Transport: healthy}}}}
	result, sample, _ := c.connectivityAccess().Playback(t.Context(), 42, playbacksource.Entries()[0])
	if result.State != "available" || sample == nil {
		t.Fatal("shared access fixture not healthy", result.State)
	}
	if err := c.controlNetworkCalibration(false); err != nil {
		t.Fatal(err)
	}
	for {
		s, changed := c.networkDiagnostics.calibration.Snapshot()
		if !s.Running {
			if s.PreparedVideos != 1 {
				t.Fatalf("connectivity resolves the same local song through shared fallback but calibration does not: prepared=%d reason=%s", s.PreparedVideos, s.Reason)
			}
			break
		}
		<-changed
	}
}

func TestNetworkCalibrationReusesResourceFallbacks(t *testing.T) {
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	at := time.Now().Add(-time.Minute)
	if err := c.store.ObserveSongURL(ctx, cacheproxy.SongURL{SongID: 42, API: c.apiBase + "/Api/Songs/play", Node: "cf", QueryStartedAt: at, ObservedAt: at,
		URL: "https://play.udon.dance/files/1/42-video.mp4?e=0000000000000000000000000000002a&s=134217728"}); err != nil {
		t.Fatal(err)
	}
	var ordinary, first atomic.Int32
	loaded := make(chan struct{}, 1)
	c.client.CloseIdleConnections()
	c.client = &http.Client{Transport: &coldBusinessChannels{ready: true,
		RoundTripper: catalogTransport(func(*http.Request) (*http.Response, error) {
			ordinary.Add(1)
			return nil, io.EOF
		}),
		channels: []upstreamrequest.Candidate{
			{ID: "direct/first/192.0.2.1", Mode: "direct", Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
				first.Add(1)
				return nil, io.EOF
			})},
			{ID: "direct/second/192.0.2.2", Mode: "direct", Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "http" || r.Header.Get("Range") == "" {
					t.Error("shared resource policy or range loader was bypassed")
				}
				loaded <- struct{}{}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})},
		},
	}}
	if err := c.controlNetworkCalibration(false); err != nil {
		t.Fatal(err)
	}
	defer c.networkDiagnostics.calibration.Close()
	select {
	case <-loaded:
	case <-ctx.Done():
		t.Fatal("calibration did not reach the shared fallback", ctx.Err())
	}
	c.networkDiagnostics.calibration.Close()
	if ordinary.Load() != 0 || first.Load() != 1 || c.service != nil {
		t.Fatalf("resource access was not shared: ordinary=%d first=%d engine=%t", ordinary.Load(), first.Load(), c.service != nil)
	}
}

func TestNetworkCalibrationSharesMetadataTraversal(t *testing.T) {
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	catalog := cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261031000000"}
	for id := int64(1); id <= 6; id++ {
		catalog.Songs = append(catalog.Songs, cacheproxy.CatalogSong{ID: id})
	}
	if err := c.store.SyncCatalog(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	ranked := c.metadataSongCandidates(ctx)
	c.mu.Lock()
	metadata := c.metadataLocked()
	c.mu.Unlock()
	if id := metadata.SelectSong(ctx, 0); id != ranked[0] {
		t.Fatal("unexpected monitoring selection", id, ranked)
	}
	started := make(chan int64, 6)
	c.client.CloseIdleConnections()
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		var id int64
		fmt.Sscan(r.URL.Query().Get("id"), &id)
		started <- id
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	if err := c.controlNetworkCalibration(false); err != nil {
		t.Fatal(err)
	}
	defer c.networkDiagnostics.calibration.Close()
	seen := make(map[int64]bool)
	for range 4 {
		select {
		case id := <-started:
			seen[id] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	c.networkDiagnostics.calibration.Close()
	for _, id := range ranked[1:5] {
		if !seen[id] {
			t.Fatal("calibration restarted the monitoring traversal", ranked, seen)
		}
	}
	if id := metadata.SelectSong(ctx, 0); id != ranked[5] {
		t.Fatal("calibration attempts did not advance shared progress", id, ranked)
	}
}

func TestNetworkCalibrationRejectsFailedSavedURLs(t *testing.T) {
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	at := time.Now().Add(-time.Minute)
	for id := int64(1); id <= 16; id++ {
		o := cacheproxy.SongURL{SongID: id, API: c.apiBase + "/Api/Songs/play", Node: "cf", QueryStartedAt: at, ObservedAt: at,
			URL: fmt.Sprintf("http://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=134217728", id, id)}
		if err := c.store.ObserveSongURL(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	failed := make(map[string]bool)
	c.client.CloseIdleConnections()
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		failed[r.URL.Query().Get("e")] = true
		mu.Unlock()
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("not found"))}, nil
	})}
	if err := c.controlNetworkCalibration(false); err != nil {
		t.Fatal(err)
	}
	for {
		s, changed := c.networkDiagnostics.calibration.Snapshot()
		if !s.Running {
			if s.Failures == 0 {
				t.Fatal("fixture did not attempt media")
			}
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	urls, err := c.store.RecentSongURLs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, o := range urls {
		if failed[fmt.Sprintf("%032x", o.SongID)] {
			t.Fatalf("URL that returned HTTP 404 remains eligible for the next run: song=%d eligible=%d", o.SongID, len(urls))
		}
	}
}
