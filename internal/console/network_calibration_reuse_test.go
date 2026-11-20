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
		if s.PreparedVideos == 1 {
			c.networkDiagnostics.calibration.Cancel()
			c.networkDiagnostics.calibration.Close()
			break
		}
		if !s.Running {
			t.Fatalf("connectivity resolves the same local song through shared fallback but calibration does not: prepared=%d reason=%s", s.PreparedVideos, s.Reason)
		}
		<-changed
	}
}

func TestNetworkCalibrationSharesRoutesWithNextDownload(t *testing.T) {
	c := testConsole(t)
	c.settings.VideoStorageMode = "unlimited"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for id := int64(1); id <= 16; id++ {
		if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: id, Node: "cf",
			URL: fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=1048576", id, id)}); err != nil {
			t.Fatal(err)
		}
	}
	const host = "play.udon.dance"
	const nextPath = "/files/1/17-video.mp4"
	var ordinary, first, healthy, business atomic.Int32
	nextBlock := make(chan struct{})
	c.client.CloseIdleConnections()
	channels := &coldBusinessChannels{
		RoundTripper: catalogTransport(func(*http.Request) (*http.Response, error) {
			ordinary.Add(1)
			return nil, io.EOF
		}),
		channels: []upstreamrequest.Candidate{
			{Host: host, IP: "192.0.2.1", Mode: "direct", Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
				first.Add(1)
				return nil, io.EOF
			})},
			{Host: host, IP: "192.0.2.2", Mode: "direct", Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "http" || r.Header.Get("Range") != "bytes=0-1048575" {
					t.Error("shared resource policy or range loader was bypassed")
				}
				if r.URL.Path == nextPath {
					business.Add(1)
				} else if healthy.Add(1) == 2 {
					close(nextBlock)
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				// Give the in-memory response a measurable network duration.
				time.Sleep(5 * time.Millisecond)
				return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-1048575/1048576"}},
					ContentLength: 1 << 20, Body: io.NopCloser(io.LimitReader(calibrationZeroReader{}, 1<<20))}, nil
			})},
		},
	}
	c.client = &http.Client{Transport: channels}
	if err := c.controlNetworkCalibration(false); err != nil {
		t.Fatal(err)
	}
	defer c.networkDiagnostics.calibration.Close()
	select {
	case <-nextBlock:
	case <-ctx.Done():
		t.Fatal("calibration did not reach the shared fallback", ctx.Err())
	}
	c.networkDiagnostics.calibration.Close()
	rows := c.downloadRoutes.Snapshot()
	if len(rows) != 2 || rows[0].IP != "192.0.2.2" || rows[0].Failed || rows[0].BlockDuration <= 0 ||
		rows[0].Source != "download" || rows[1].IP != "192.0.2.1" || !rows[1].Failed || c.service != nil {
		t.Fatalf("unexpected routes after canceling calibration: %+v, engine=%t", rows, c.service != nil)
	}
	c.lifecycleMu.Lock()
	engine, err := c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// MD5 of the complete 1 MiB zero-filled response.
	if _, err := engine.Prefetch(ctx, "http://"+host+nextPath+"?e=b6d81b360a5672d80c27430f39153e2c&s=1048576"); err != nil {
		t.Fatal(err)
	}
	if first.Load() != 1 || ordinary.Load() != 0 || business.Load() != 1 {
		t.Fatalf("business download did not reuse the healthy IP: first=%d ordinary=%d business=%d", first.Load(), ordinary.Load(), business.Load())
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

func TestNetworkCalibrationKeepsFailedSavedURLs(t *testing.T) {
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for id := int64(1); id <= 16; id++ {
		o := cacheproxy.PlaybackObservation{SongID: id, Node: "cf",
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
	urls, err := c.store.AvailableMediaURLs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failed) == 0 || len(urls) != 16 {
		t.Fatal("failed download removed addresses", len(failed), len(urls))
	}
}
