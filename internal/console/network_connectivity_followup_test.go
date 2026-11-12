package console

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamcheck"
)

func runFollowupConnectivity(t *testing.T, c *Console) networkConnectivityStatus {
	t.Helper()
	if err := c.controlNetworkConnectivity(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connectivityDoneForTest(c.networkDiagnostics):
	case <-time.After(5 * time.Second):
		t.Fatal("connectivity check did not finish")
	}
	return connectivitySnapshot(c)
}

func TestConnectivityCompleteCatalogRotatesPlaybackSamples(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	ctx := t.Context()
	ids := []int64{900001, 900002}
	if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000", Songs: []cacheproxy.CatalogSong{{ID: ids[0]}, {ID: ids[1]}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		url := fmt.Sprintf("http://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=1234", id, id)
		if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: id, Node: "cf", URL: url}); err != nil {
			t.Fatal(err)
		}
	}
	if pending := c.metadataSongCandidates(ctx); len(pending) != 0 {
		t.Fatal("fixture requires complete metadata", pending)
	}
	var requested atomic.Int64
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		if r.URL.Path == "/Api/Songs/play" {
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			requested.Store(id)
			if id == ids[0] {
				resp.StatusCode = http.StatusServiceUnavailable
				resp.Header.Del("Location")
			}
		}
		return resp, nil
	})}
	for _, want := range []int64{ids[0], ids[1], ids[0], ids[1]} {
		state := runFollowupConnectivity(t, c)
		if got := requested.Load(); got != want {
			t.Fatalf("sample did not rotate: got %d, want %d", got, want)
		}
		row := slices.IndexFunc(state.Results, func(r networkConnectivityResult) bool { return r.Operation == upstreamcheck.PlaybackURL })
		if row < 0 || (want == ids[0] && state.Results[row].State != "failed") || (want == ids[1] && state.Results[row].State != "available") {
			t.Fatal("playback result did not reflect the selected song", state.Results)
		}
	}
}

func TestConnectivityRetryCanceledByStorageFailureKeepsCurrentResourceObservation(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	ctx := t.Context()
	if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000", Songs: []cacheproxy.CatalogSong{{ID: 900001}}}); err != nil {
		t.Fatal(err)
	}
	const oldMD5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const freshMD5 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldURL := "http://play.udon.dance/files/1/900001-old.mp4?e=" + oldMD5 + "&s=1234"
	freshURL := "http://play.udon.dance/files/1/900001-new.mp4?e=" + freshMD5 + "&s=1234"
	if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: 900001, Node: "cf", URL: oldURL}); err != nil {
		t.Fatal(err)
	}
	var failStorage atomic.Bool
	retryStarted := make(chan struct{})
	faultInjected := make(chan struct{})
	var retryOnce sync.Once
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		switch {
		case r.URL.Path == "/Api/Songs/list" && failStorage.Load():
			select {
			case <-retryStarted:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			select {
			case <-faultInjected:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			resp.Body.Close()
			resp.Body = io.NopCloser(strings.NewReader(`{"time":"20261101000000","groups":{"contents":[{"songInfos":[{"id":900001}]}]}}`))
		case r.URL.Path == "/Api/Songs/play":
			if failStorage.Load() {
				resp.Header.Set("Location", freshURL)
			} else {
				resp.Header.Set("Location", oldURL)
			}
		case r.Header.Get("Range") != "" && failStorage.Load():
			if r.URL.Query().Get("e") == oldMD5 {
				resp.StatusCode, resp.Body = http.StatusForbidden, io.NopCloser(strings.NewReader(""))
			} else {
				retryOnce.Do(func() { close(retryStarted) })
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		}
		return resp, nil
	})}
	previous := runFollowupConnectivity(t, c)
	if err := c.metadata.CatalogError(); err != "" {
		t.Fatal("baseline catalog failed", err)
	}
	i := slices.IndexFunc(previous.Results, func(r networkConnectivityResult) bool {
		return r.Operation == upstreamcheck.Resource && r.State == "available"
	})
	if i < 0 {
		t.Fatal("missing previous resource observation", previous.Results)
	}
	failStorage.Store(true)
	if err := c.controlNetworkConnectivity(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("resource retry did not start")
	}
	func() {
		// A resource retry can begin before the parallel playback observation
		// commits. Wait for its lifecycle boundary before occupying the journal.
		c.lifecycleMu.Lock()
		defer c.lifecycleMu.Unlock()
		failDatabaseWrites(t, c.settings.DatabasePath)
	}()
	close(faultInjected)
	select {
	case <-connectivityDoneForTest(c.networkDiagnostics):
	case <-time.After(5 * time.Second):
		t.Fatal("connectivity check did not finish after storage failure")
	}
	current := connectivitySnapshot(c)
	var resources []networkConnectivityResult
	for _, row := range current.Results {
		if row.Operation == upstreamcheck.Resource {
			resources = append(resources, row)
		}
	}
	if len(resources) != 1 || resources[0].State != "failed" || resources[0].Retained || !resources[0].ObservedAt.After(previous.Results[i].ObservedAt) {
		t.Fatal("the current failed observation was lost during the retry", resources, previous.Results[i])
	}
}

func TestConnectivitySkipsOversizedCachedResourceAndUsesNext(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	ctx := t.Context()
	if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000", Songs: []cacheproxy.CatalogSong{{ID: 900001}, {ID: 900002}}}); err != nil {
		t.Fatal(err)
	}
	const bigMD5, smallMD5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, observation := range []cacheproxy.PlaybackObservation{
		{SongID: 900001, Node: "cf", URL: "http://play.udon.dance/files/1/900001-big.mp4?e=" + bigMD5 + "&s=2147483649"},
		{SongID: 900002, Node: "cf", URL: "http://play.udon.dance/files/1/900002-small.mp4?e=" + smallMD5 + "&s=1234"},
	} {
		if err := c.store.ObserveSongURL(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	var resourceMD5 atomic.Value
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		if r.URL.Path == "/Api/Songs/play" {
			resp.StatusCode = http.StatusServiceUnavailable
			resp.Header.Del("Location")
		}
		if r.Header.Get("Range") != "" {
			resourceMD5.Store(r.URL.Query().Get("e"))
		}
		return resp, nil
	})}
	state := runFollowupConnectivity(t, c)
	if got := resourceMD5.Load(); got != smallMD5 {
		t.Fatal("did not check the eligible saved resource", got, state.Results)
	}
	if !slices.ContainsFunc(state.Results, func(row networkConnectivityResult) bool {
		return row.Operation == upstreamcheck.Resource && row.State == "available"
	}) {
		t.Fatal("eligible resource observation missing", state.Results)
	}
}
