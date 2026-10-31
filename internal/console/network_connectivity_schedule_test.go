package console

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

func automaticTestConsole(t *testing.T) *Console {
	t.Helper()
	// Each fake-clock bubble must own its channel notifications. Restore the
	// process-global legacy channel only after Console cleanup has drained.
	previous := upstreamrequest.Default
	upstreamrequest.Default = upstreamrequest.NewChannel()
	t.Cleanup(func() { upstreamrequest.Default = previous })
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	return c
}

func automaticConnectivityResponse(r *http.Request) *http.Response {
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Request: r}
	body := ""
	switch r.URL.Path {
	case "/Api/Songs/list":
		body = `{"time":"20261031000000","groups":{"contents":[{"songInfos":[{"id":900001},{"id":900002}]}]}}`
	case "/api/v2/wanna/songs", "/api/wannaInfo":
		body = `{"code":200,"data":{"time":"20261031000000","groups":[{"entries":[{"id":900001,"checksum":"000000000000000000000000000dbba1"},{"id":900002,"checksum":"000000000000000000000000000dbba2"}]}]}}`
	case "/Api/Songs/play":
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		resp.StatusCode = http.StatusFound
		resp.Header.Set("Location", fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=1234", id, id))
	default:
		resp.StatusCode, body = http.StatusPartialContent, "x"
		resp.Header.Set("Content-Range", "bytes 0-0/1234")
	}
	resp.Body = io.NopCloser(strings.NewReader(body))
	return resp
}

func connectivitySnapshot(c *Console) networkConnectivityStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.networkDiagnostics.connectivity
}

func TestAutomaticConnectivitySharesManualFlowAndProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := automaticTestConsole(t)
		var songsMu sync.Mutex
		var songs []int64
		requested := func() []int64 { songsMu.Lock(); defer songsMu.Unlock(); return slices.Clone(songs) }
		var catalogs, resources atomic.Int32
		c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
			resp := automaticConnectivityResponse(r)
			if r.URL.Path == "/Api/Songs/play" {
				id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
				songsMu.Lock()
				songs = append(songs, id)
				songsMu.Unlock()
				if id == 900002 {
					resp.StatusCode = http.StatusServiceUnavailable
				}
			} else if r.Header.Get("Range") != "" {
				resources.Add(1)
				if r.Header.Get("Range") != "bytes=0-0" {
					t.Error("automatic check downloaded a speed sample")
				}
			} else {
				catalogs.Add(1)
			}
			return resp, nil
		})}
		if err := c.StartUpstreamServices(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		first := connectivitySnapshot(c)
		if first.Running || first.Initializing || first.NextCheck.Sub(first.Finished) != 5*time.Minute || !slices.Equal(requested(), []int64{900002}) {
			t.Fatal(first, requested())
		}
		time.Sleep(2 * time.Minute)
		if err := c.controlNetworkConnectivity(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		manual := connectivitySnapshot(c)
		if manual.Run != first.Run+1 || !manual.Finished.After(first.Finished) || manual.NextCheck.Sub(manual.Finished) != 5*time.Minute {
			t.Fatal("manual round did not publish/reset the shared schedule", manual)
		}
		for _, row := range manual.Results {
			if row.Operation == upstreamcheck.Catalog && row.Entry != "api" {
				if row.State != "skipped" || row.LatencyMS != nil || !row.ObservedAt.IsZero() {
					t.Fatal(row)
				}
				continue
			}
			if row.State != "available" || row.LatencyMS == nil || row.ObservedAt.Before(first.Finished) {
				t.Fatal("manual completion did not publish current observations", row)
			}
		}
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if connectivitySnapshot(c).Run != manual.Run {
			t.Fatal("old automatic deadline survived manual completion")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if !slices.Equal(requested(), []int64{900002, 900001, 900002}) || catalogs.Load() != 3 || resources.Load() != 1 {
			t.Fatal("triggers did not share one traversal/response", requested(), catalogs.Load(), resources.Load())
		}
		if pending := c.metadataSongCandidates(t.Context()); !slices.Equal(pending, []int64{900002}) {
			t.Fatal("same response did not complete the missing metadata", pending)
		}
		speed, _ := c.networkDiagnostics.calibration.Snapshot()
		if speed.Run != 0 || c.monitor != nil || testDNS(c).queries.Load() != 0 {
			t.Fatal("automatic lightweight check escaped its scope", speed.Run)
		}
		c.BeginShutdown()
		last := connectivitySnapshot(c)
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if !last.NextCheck.IsZero() || connectivitySnapshot(c).Run != last.Run {
			t.Fatal("shutdown retained automatic work")
		}
	})
}

func TestAutomaticConnectivityStopsOnStorageFailureAndBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := automaticTestConsole(t)
		db, err := sql.Open("sqlite", c.settings.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec("CREATE TRIGGER fail_detection_catalog BEFORE INSERT ON songs BEGIN SELECT RAISE(FAIL, 'test storage failure'); END"); err != nil {
			t.Fatal(err)
		}
		var catalogs, playback atomic.Int32
		c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/Api/Songs/play" {
				playback.Add(1)
			} else if r.Header.Get("Range") == "" {
				catalogs.Add(1)
			}
			return automaticConnectivityResponse(r), nil
		})}
		if err := c.StartUpstreamServices(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		first := connectivitySnapshot(c)
		for _, row := range first.Results {
			if row.Entry != "api" {
				if !row.ObservedAt.IsZero() {
					t.Fatal(row)
				}
				continue
			}
			if row.State != "available" || row.LatencyMS == nil || row.ObservedAt.IsZero() {
				t.Fatal("storage failure changed a network result", row)
			}
		}
		if c.store.Error() == nil {
			t.Fatal("storage failure not independently retained")
		}
		for _, delay := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute} {
			state := connectivitySnapshot(c)
			if !state.Initializing || state.Running || state.NextCheck.Sub(state.Finished) != delay {
				t.Fatal(delay, state)
			}
			before := catalogs.Load()
			time.Sleep(delay - time.Nanosecond)
			synctest.Wait()
			if catalogs.Load() != before {
				t.Fatal("early initialization retry")
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			if catalogs.Load() != before+1 || playback.Load() != 0 {
				t.Fatal("initialization retry checked non-catalog entries", catalogs.Load(), playback.Load())
			}
			for i, row := range connectivitySnapshot(c).Results {
				if row.Operation == upstreamcheck.Catalog && row.Entry == "api" {
					if !row.ObservedAt.After(state.Finished) {
						t.Fatal("catalog result not refreshed", row)
					}
				} else if !row.ObservedAt.Equal(first.Results[i].ObservedAt) {
					t.Fatal("retry refreshed an unchecked observation", row)
				}
			}
		}
	})
}

func TestAutomaticConnectivityCatalogRecoveryReturnsToNormalPeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := automaticTestConsole(t)
		var available atomic.Bool
		var playback atomic.Int32
		c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
			resp := automaticConnectivityResponse(r)
			if !available.Load() {
				resp.StatusCode = http.StatusServiceUnavailable
			}
			if r.URL.Path == "/Api/Songs/play" {
				playback.Add(1)
			}
			return resp, nil
		})}
		if err := c.StartUpstreamServices(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !connectivitySnapshot(c).Initializing {
			t.Fatal("unavailable catalog did not enter initialization")
		}
		available.Store(true)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		state := connectivitySnapshot(c)
		if state.Initializing || state.NextCheck.Sub(state.Finished) != 5*time.Minute || playback.Load() != 0 {
			t.Fatal("accepted catalog did not finish initialization", state)
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if playback.Load() != 1 {
			t.Fatal("normal cycle did not resume metadata repair", playback.Load())
		}
	})
}

func TestAutomaticConnectivityRunsAlongsideSpeedAndSharesManualRound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := automaticTestConsole(t)
		seedCalibrationSong(t, c)
		stubNetworkDiagnostics(t, c) // Requests block until canceled.
		if err := c.controlNetworkCalibration(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := c.StartUpstreamServices(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		first := connectivitySnapshot(c)
		speed, _ := c.networkDiagnostics.calibration.Snapshot()
		if !first.Running || first.Run != 1 || !speed.Running {
			t.Fatal("automatic check did not run alongside the speed test", first, speed.Running)
		}
		if err := c.controlNetworkConnectivity(false); err != nil {
			t.Fatal("manual entry did not join automatic round", err)
		}
		if connectivitySnapshot(c).Run != first.Run {
			t.Fatal("manual entry duplicated automatic work")
		}
		if err := c.controlNetworkConnectivity(true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		state := connectivitySnapshot(c)
		if state.Running || state.Phase != "canceled" || !state.Finished.IsZero() || state.NextCheck.Sub(time.Now()) != 5*time.Minute {
			t.Fatal("cancel did not discard only the current round", state)
		}
		speed, _ = c.networkDiagnostics.calibration.Snapshot()
		if !speed.Running {
			t.Fatal("canceling connectivity stopped the speed test")
		}
		c.BeginShutdown()
	})
}
