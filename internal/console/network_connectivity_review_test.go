package console

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamcheck"
)

func TestConnectivityReviewEarlyStorageFailureRetainsUnobservedResults(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	var failCatalog atomic.Bool
	var playback, resource atomic.Int32
	playbackStarted, resourceStarted := make(chan struct{}, 1), make(chan struct{}, 1)
	faultReady, releaseFault := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseFault:
		default:
			close(releaseFault)
		}
	}()
	var waitForPeers atomic.Bool
	waitForPeers.Store(true)
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		if r.URL.Path == "/Api/Songs/play" {
			playback.Add(1)
			if failCatalog.Load() {
				playbackStarted <- struct{}{}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		} else if r.Header.Get("Range") != "" {
			resource.Add(1)
			if failCatalog.Load() {
				resourceStarted <- struct{}{}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		} else if failCatalog.Load() {
			if waitForPeers.Swap(false) {
				<-playbackStarted
				<-resourceStarted
				close(faultReady)
				select {
				case <-releaseFault:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			resp.Body.Close()
			resp.Body = io.NopCloser(strings.NewReader(`{"time":"20261101000000","groups":{"contents":[{"songInfos":[{"id":900001},{"id":900002}]}]}}`))
		}
		return resp, nil
	})}
	run := func() {
		t.Helper()
		if err := c.controlNetworkConnectivity(false); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.networkDiagnostics.connectivityDone:
		case <-time.After(5 * time.Second):
			t.Fatal("connectivity check did not finish")
		}
	}
	run()
	before := connectivitySnapshot(c)
	counts := [2]int32{playback.Load(), resource.Load()}
	if counts[0] == 0 || counts[1] == 0 {
		t.Fatal("fixture needs previous observations", before.Results)
	}
	db, err := sql.Open("sqlite", c.settings.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER reject_catalog BEFORE DELETE ON catalog_members BEGIN SELECT RAISE(ABORT,'catalog write rejected'); END"); err != nil {
		t.Fatal(err)
	}
	failCatalog.Store(true)
	for attempt := range 2 {
		var previousSnapshot networkConnectivityStatus
		if attempt == 0 {
			if err := c.controlNetworkConnectivity(false); err != nil {
				t.Fatal(err)
			}
			select {
			case <-faultReady:
			case <-time.After(5 * time.Second):
				t.Fatal("parallel checks did not start")
			}
			func() {
				c.lifecycleMu.Lock()
				defer c.lifecycleMu.Unlock()
				close(releaseFault)
				for {
					c.mu.Lock()
					current, changed := c.networkDiagnostics.connectivity, c.networkDiagnostics.connectivityChanged
					c.mu.Unlock()
					if slices.ContainsFunc(current.Results, func(row networkConnectivityResult) bool {
						return row.Operation == upstreamcheck.Catalog && row.State == "available"
					}) {
						previousSnapshot = current
						break
					}
					select {
					case <-changed:
					case <-time.After(5 * time.Second):
						t.Fatal("catalog observation was not published")
					}
				}
			}()
			select {
			case <-c.networkDiagnostics.connectivityDone:
			case <-time.After(5 * time.Second):
				t.Fatal("connectivity check did not finish")
			}
		} else {
			run()
		}
		after := connectivitySnapshot(c)
		if !strings.Contains(c.metadata.CatalogError(), "catalog write rejected") {
			t.Fatal("catalog storage failure did not stop the full check", after.Results, c.metadata.CatalogError())
		}
		if slices.ContainsFunc(after.Results, func(row networkConnectivityResult) bool { return row.State == "checking" }) {
			t.Fatal("canceled sibling checks remained in progress after the round ended", after.Results)
		}
		for _, operation := range []upstreamcheck.Operation{upstreamcheck.PlaybackURL, upstreamcheck.Resource} {
			if attempt == 0 && !slices.ContainsFunc(previousSnapshot.Results, func(row networkConnectivityResult) bool { return row.Operation == operation && row.State == "checking" }) {
				t.Fatal("an already published snapshot was mutated", operation, previousSnapshot.Results)
			}
			old := slices.IndexFunc(before.Results, func(row networkConnectivityResult) bool {
				return row.Operation == operation && !row.ObservedAt.IsZero()
			})
			if old < 0 {
				t.Fatal("missing prior observation", operation)
			}
			observed := before.Results[old]
			matching := 0
			for _, row := range after.Results {
				if row.Operation == operation && row.Entry == observed.Entry && row.ObservedAt.Equal(observed.ObservedAt) && row.Retained {
					matching++
				}
			}
			if matching != 1 {
				t.Fatal("unobserved result was lost or duplicated", operation, after.Results)
			}
		}
	}
}

func TestConnectivityReviewCatalogSampleAndBusinessUpdates(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	ctx := t.Context()
	const oldMD5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ids := []int64{900001, 900002}
	if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000", Songs: []cacheproxy.CatalogSong{{ID: ids[0]}, {ID: ids[1]}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		url := fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%s&s=1234", id, oldMD5)
		if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: id, Node: "cf", URL: url}); err != nil {
			t.Fatal(err)
		}
	}
	if candidates := c.metadataSongCandidates(ctx); len(candidates) != 0 {
		t.Fatal("fixture must start with complete song metadata", candidates)
	}
	var catalogAvailable atomic.Bool
	var playback, resource atomic.Int32
	var requestedSong atomic.Int64
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		if r.URL.Path == "/Api/Songs/play" {
			call := playback.Add(1)
			var id int64
			_, _ = fmt.Sscan(r.URL.Query().Get("id"), &id)
			requestedSong.Store(id)
			if call == 3 {
				resp.Header.Set("Location", fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&s=1234", id))
			}
		} else if r.Header.Get("Range") != "" {
			resource.Add(1)
		} else if !catalogAvailable.Load() {
			resp.StatusCode = http.StatusServiceUnavailable
		}
		return resp, nil
	})}
	run := func(operation upstreamcheck.Operation) {
		t.Helper()
		if err := c.controlNetworkConnectivityOperation(false, operation); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.networkDiagnostics.connectivityDone:
		case <-time.After(5 * time.Second):
			t.Fatal("connectivity check did not finish")
		}
	}
	run("")
	if playback.Load() != 1 || resource.Load() != 1 {
		t.Fatal("cached samples did not allow checks despite the failed catalog")
	}
	catalogAvailable.Store(true)
	run(upstreamcheck.Catalog)
	status, err := c.store.CatalogStatus(ctx)
	if err != nil || status.Revision != "20261031000000" || playback.Load() != 1 {
		t.Fatal("catalog-only check did not update the business catalog independently", status, err)
	}
	run(upstreamcheck.PlaybackURL)
	id := requestedSong.Load()
	if playback.Load() != 2 || !slices.Contains(ids, id) {
		t.Fatalf("playback check did not reuse the successful catalog sample: calls=%d song=%d", playback.Load(), id)
	}
	urls, err := c.store.SongURLs(ctx, id, "cf")
	if err != nil || len(urls) != 1 || urls[0].MD5 != fmt.Sprintf("%032x", id) {
		t.Fatal("playback observation did not update song/media mapping and URL", urls, err)
	}
	run(upstreamcheck.Resource)
	if playback.Load() != 3 || resource.Load() != 2 {
		t.Fatal("resource check did not reuse the sample and resolve its address", playback.Load(), resource.Load())
	}
	urls, err = c.store.SongURLs(ctx, id, "cf")
	if err != nil || len(urls) != 1 || urls[0].MD5 != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatal("resource preparation did not reuse the playback metadata write", urls, err)
	}
}

func TestConnectivityReviewSkippedResourceRetainsObservedResult(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	var failPlayback atomic.Bool
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		if r.URL.Path == "/Api/Songs/play" && failPlayback.Load() {
			resp.StatusCode = http.StatusServiceUnavailable
			resp.Header.Del("Location")
		}
		return resp, nil
	})}
	run := func(operation upstreamcheck.Operation) {
		t.Helper()
		if err := c.controlNetworkConnectivityOperation(false, operation); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.networkDiagnostics.connectivityDone:
		case <-time.After(5 * time.Second):
			t.Fatal("connectivity check did not finish")
		}
	}
	run("")
	before := connectivitySnapshot(c)
	i := slices.IndexFunc(before.Results, func(row networkConnectivityResult) bool {
		return row.Operation == upstreamcheck.Resource && !row.ObservedAt.IsZero()
	})
	if i < 0 {
		t.Fatal("fixture has no resource observation", before.Results)
	}
	observed := before.Results[i]
	failPlayback.Store(true)
	for _, operation := range []upstreamcheck.Operation{upstreamcheck.Resource, upstreamcheck.Resource} {
		run(operation)
		state := connectivitySnapshot(c)
		old := slices.IndexFunc(state.Results, func(row networkConnectivityResult) bool {
			return row.Operation == upstreamcheck.Resource && row.Entry == observed.Entry && row.ObservedAt.Equal(observed.ObservedAt)
		})
		skipped := slices.ContainsFunc(state.Results, func(row networkConnectivityResult) bool {
			return row.Operation == upstreamcheck.Resource && row.State == "skipped"
		})
		if old < 0 || !skipped || !state.Results[old].Retained {
			t.Fatal("skipped check lost the previous observation or omitted the skipped result", operation, state.Results)
		}
	}
	run("")
	full := connectivitySnapshot(c)
	if !slices.ContainsFunc(full.Results, func(row networkConnectivityResult) bool {
		return row.Operation == upstreamcheck.Resource && row.State == "available" && !row.Retained
	}) {
		t.Fatal("full check did not use the cached resource while playback failed", full.Results)
	}
	failPlayback.Store(false)
	run(upstreamcheck.Resource)
	state := connectivitySnapshot(c)
	if !slices.ContainsFunc(state.Results, func(row networkConnectivityResult) bool {
		return row.Operation == upstreamcheck.Resource && row.State == "available" && !row.Retained
	}) || slices.ContainsFunc(state.Results, func(row networkConnectivityResult) bool {
		return row.Operation == upstreamcheck.Resource && (row.State == "skipped" || row.Retained)
	}) {
		t.Fatal("a new resource observation did not replace the skipped history", state.Results)
	}
}

func TestConnectivityReviewCatalogOnlyDoesNotAdvanceTraversal(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	var playback atomic.Int32
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/play" {
			playback.Add(1)
		}
		return automaticConnectivityResponse(r), nil
	})}
	if err := c.controlNetworkConnectivityOperation(false, upstreamcheck.Catalog); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.networkDiagnostics.connectivityDone:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog check did not finish")
	}
	pending := c.metadataSongCandidates(t.Context())
	if len(pending) < 2 || playback.Load() != 0 {
		t.Fatal("fixture requires multiple unattempted metadata candidates", pending, playback.Load())
	}
	c.mu.Lock()
	metadata := c.metadata
	c.mu.Unlock()
	if got := metadata.SelectSong(t.Context(), 0); got != pending[0] {
		t.Fatal("catalog-only check advanced metadata traversal", got, pending)
	}
}

func TestConnectivityReviewManualDeadlineIsExact(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) { return automaticConnectivityResponse(r), nil })}
	c.mu.Lock()
	_, err := c.networkCalibrationLocked()
	if err != nil {
		c.mu.Unlock()
		t.Fatal(err)
	}
	c.networkDiagnostics.connectivityAuto = true
	c.scheduleConnectivityLocked(5 * time.Minute)
	deadline := c.networkDiagnostics.connectivity.NextCheck
	c.mu.Unlock()
	for i := 0; i < 3; i++ {
		if err := c.controlNetworkConnectivityOperation(false, upstreamcheck.Catalog); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.networkDiagnostics.connectivityDone:
		case <-time.After(5 * time.Second):
			t.Fatal("connectivity check did not finish")
		}
		if actual := connectivitySnapshot(c).NextCheck; !actual.Equal(deadline) {
			t.Fatalf("single check %d shifted the scheduled deadline by %s", i+1, actual.Sub(deadline))
		}
	}
}
