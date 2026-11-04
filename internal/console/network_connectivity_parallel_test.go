package console

import (
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

func TestNetworkConnectivityFullRunsThreeRequestsTogether(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		name := "completes"
		if cancelRun {
			name = "cancels"
		}
		t.Run(name, func(t *testing.T) {
			c := testConsole(t)
			c.settings.UpstreamMode = "proxy"
			ctx := t.Context()
			if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000", Songs: []cacheproxy.CatalogSong{{ID: 900001}}}); err != nil {
				t.Fatal(err)
			}
			const cached = "http://play.udon.dance/files/1/900001-video.mp4?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=1234"
			if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: 900001, Node: "cf", URL: cached}); err != nil {
				t.Fatal(err)
			}
			started, canceled := make(chan upstreamcheck.Operation, 3), make(chan upstreamcheck.Operation, 3)
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
				op := upstreamcheck.Catalog
				if r.URL.Path == "/Api/Songs/play" {
					op = upstreamcheck.PlaybackURL
				} else if r.Header.Get("Range") != "" {
					op = upstreamcheck.Resource
				}
				started <- op
				select {
				case <-release:
					return automaticConnectivityResponse(r), nil
				case <-r.Context().Done():
					canceled <- op
					return nil, r.Context().Err()
				}
			})}
			if err := c.controlNetworkConnectivity(false); err != nil {
				t.Fatal(err)
			}
			seen := make(map[upstreamcheck.Operation]bool)
			for range 3 {
				select {
				case op := <-started:
					seen[op] = true
				case <-time.After(5 * time.Second):
					t.Fatal("not all requests started before any completed", seen)
				}
			}
			if !seen[upstreamcheck.Catalog] || !seen[upstreamcheck.PlaybackURL] || !seen[upstreamcheck.Resource] {
				t.Fatal(seen)
			}
			state := connectivitySnapshot(c)
			for _, op := range []upstreamcheck.Operation{upstreamcheck.Catalog, upstreamcheck.PlaybackURL, upstreamcheck.Resource} {
				if !slices.ContainsFunc(state.Results, func(row networkConnectivityResult) bool { return row.Operation == op && row.State == "checking" }) {
					t.Fatal("shared status did not show all three running", op, state.Results)
				}
			}
			if cancelRun {
				if err := c.controlNetworkConnectivity(true); err != nil {
					t.Fatal(err)
				}
			} else {
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case <-connectivityDoneForTest(c.networkDiagnostics):
			case <-time.After(5 * time.Second):
				t.Fatal("check did not finish")
			}
			state = connectivitySnapshot(c)
			if cancelRun {
				if state.Phase != "canceled" || len(state.Results) != 0 || len(canceled) != 3 {
					t.Fatal("cancel did not drain every request", state, len(canceled))
				}
				return
			}
			for _, op := range []upstreamcheck.Operation{upstreamcheck.Catalog, upstreamcheck.PlaybackURL, upstreamcheck.Resource} {
				if !slices.ContainsFunc(state.Results, func(row networkConnectivityResult) bool { return row.Operation == op && row.State == "available" }) {
					t.Fatal("missing observation", op, state.Results)
				}
			}
			urls, err := c.store.SongURLs(ctx, 900001, "cf")
			if err != nil || len(urls) != 1 || urls[0].MD5 != "000000000000000000000000000dbba1" {
				t.Fatal("playback response was not saved", urls, err)
			}
		})
	}
}

func TestNetworkConnectivityRetriesStaleResourceAfterPlaybackUpdate(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	ctx := t.Context()
	if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Source: cacheproxy.UdonCatalogURL, Revision: "20261030000000", Songs: []cacheproxy.CatalogSong{{ID: 900001}}}); err != nil {
		t.Fatal(err)
	}
	const oldMD5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const freshMD5 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: 900001, Node: "cf", URL: "http://play.udon.dance/files/1/900001-old.mp4?e=" + oldMD5 + "&s=1234"}); err != nil {
		t.Fatal(err)
	}
	oldStarted := make(chan struct{})
	var oldOnce sync.Once
	var requestedSong atomic.Int64
	c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
		resp := automaticConnectivityResponse(r)
		if r.URL.Path == "/Api/Songs/play" {
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			requestedSong.Store(id)
			select {
			case <-oldStarted:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			resp.Header.Set("Location", "https://play.udon.dance/files/1/900001-new.mp4?e="+freshMD5+"&s=1234")
		} else if r.Header.Get("Range") != "" && r.URL.Query().Get("e") == oldMD5 {
			oldOnce.Do(func() { close(oldStarted) })
			resp.StatusCode, resp.Body = http.StatusForbidden, io.NopCloser(strings.NewReader(""))
		}
		return resp, nil
	})}
	if err := c.controlNetworkConnectivity(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connectivityDoneForTest(c.networkDiagnostics):
	case <-time.After(5 * time.Second):
		t.Fatal("check did not finish")
	}
	state := connectivitySnapshot(c)
	var resourceRows []networkConnectivityResult
	for _, row := range state.Results {
		if row.Operation == upstreamcheck.Resource {
			resourceRows = append(resourceRows, row)
		}
	}
	if len(resourceRows) != 1 || resourceRows[0].State != "available" || resourceRows[0].Retained {
		t.Fatal("fresh resource did not replace the expired observation", resourceRows)
	}
	id := requestedSong.Load()
	urls, err := c.store.SongURLs(ctx, id, "cf")
	if err != nil || len(urls) != 1 || urls[0].MD5 != freshMD5 {
		t.Fatal("fresh playback address was not saved", id, urls, err)
	}
}
