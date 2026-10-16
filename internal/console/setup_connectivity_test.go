package console

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestSetupConnectivityEventsManualCancelDiscardAndShutdown(t *testing.T) {
	c := testConsole(t)
	started := stubSetupNetwork(t, c)
	server := httptest.NewServer(c)
	defer server.Close()
	c.address = strings.TrimPrefix(server.URL, "http://")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL + "/api/setup/network/connectivity/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	read := func() setupConnectivityStatus {
		for scan.Scan() {
			if line := scan.Text(); strings.HasPrefix(line, "data: ") {
				var state setupConnectivityStatus
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &state); err != nil {
					t.Fatal(err)
				}
				return state
			}
		}
		t.Fatal("stream ended before state", scan.Err())
		return setupConnectivityStatus{}
	}
	if s := read(); s.Running || s.Run != 0 {
		t.Fatal("subscription starts work")
	}
	select {
	case <-started:
		t.Fatal("subscription made upstream request")
	default:
	}
	if err := c.controlSetupConnectivity(false, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("manual check did not start")
	}
	for s := read(); ; s = read() {
		if len(s.Results) > 0 {
			if s.Results[0].State != "checking" {
				t.Fatal(s)
			}
			break
		}
	}
	if err := c.controlSetupCalibration(false, setupCalibrationRequest{}); err == nil {
		t.Fatal("overlapping throughput accepted")
	}
	if err := c.controlSetupConnectivity(true, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	for s := read(); ; s = read() {
		if !s.Running {
			if s.Phase != "canceled" || len(s.Results) != 0 {
				t.Fatal("cancel kept results", s)
			}
			break
		}
	}
	c.BeginShutdown()
	for s := read(); !s.Closed; s = read() {
	}
}

func TestSetupConnectivityConsentOriginAndToken(t *testing.T) {
	c := testConsole(t)
	stubSetupNetwork(t, c)
	for _, kind := range []string{"start", "cancel"} {
		for _, tc := range []struct {
			token, origin string
			code          int
		}{{"", "", 403}, {c.token, "https://example.invalid", 403}} {
			r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/network/connectivity/"+kind, strings.NewReader(`{}`))
			r.Header.Set("X-SWD-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatal(w.Code)
			}
		}
	}
	c.terms = termsReceipt{}
	for _, method := range []string{"GET", "POST"} {
		path := "events"
		if method == "POST" {
			path = "start"
		}
		r := httptest.NewRequest(method, "http://"+c.address+"/api/setup/network/connectivity/"+path, strings.NewReader(`{}`))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 428 {
			t.Fatal(w.Code)
		}
	}
}

func TestSetupConnectivityUsesUnsavedSOCKS5AndCancelsConnection(t *testing.T) {
	c := testConsole(t)
	connected, closed := make(chan struct{}, 1), make(chan struct{}, 1)
	address := socksPeerWithAuth(t, "fixture", "secret", func(conn net.Conn, target string) {
		connected <- struct{}{}
		socksReply(conn, 0)
		_, _ = io.Copy(io.Discard, conn)
		closed <- struct{}{}
	})
	version, err := c.setupSettingsVersion(c.savedSettings)
	if err != nil {
		t.Fatal(err)
	}
	before, settings, business := c.client, c.settings, upstreamrequest.Default.Snapshot().Revision
	password := "secret"
	input := setupCalibrationRequest{Upstream: "proxy", Proxy: &setupPlan{BaseVersion: version, ProxyBackend: "external-socks5", SOCKS5Address: address, SOCKS5User: "fixture", Password: &password}}
	if err := c.controlSetupConnectivity(false, input); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("draft proxy not used")
	}
	if err := c.controlSetupConnectivity(true, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("proxy connection retained after cancellation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-c.setupNetwork.connectivityDone:
	case <-ctx.Done():
		t.Fatal("check did not drain")
	}
	c.mu.Lock()
	s := c.setupNetwork.connectivity
	c.mu.Unlock()
	if s.Upstream != "proxy" || s.Phase != "canceled" || len(s.Results) != 0 {
		t.Fatal(s)
	}
	if c.client != before || c.settings != settings || upstreamrequest.Default.Snapshot().Revision != business || len(c.dns.cache) != 0 {
		t.Fatal("diagnostic changed business routing or used direct DNS")
	}
}

func TestSetupConnectivityCancelRetainsCompletedResults(t *testing.T) {
	c := testConsole(t)
	stubSetupNetwork(t, c)
	started := make(chan struct{}, 1)
	// A failed connectivity conclusion is still a valid completed check.
	c.setupNetwork.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	c.setupNetwork.channel.Publish(c.setupNetwork.client.Transport)
	if err := c.controlSetupConnectivity(false, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	<-c.setupNetwork.connectivityDone
	c.mu.Lock()
	previous := c.setupNetwork.connectivity.LastValid["direct"]
	c.mu.Unlock()
	if len(previous) != 3 || previous[0].State != "failed" {
		t.Fatal("complete result not retained", previous)
	}
	c.setupNetwork.channel.Publish(catalogTransport(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	if err := c.controlSetupConnectivity(false, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.controlSetupConnectivity(true, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	<-c.setupNetwork.connectivityDone
	c.mu.Lock()
	result := c.setupNetwork.connectivity
	c.mu.Unlock()
	if result.Phase != "canceled" || len(result.Results) != 0 || len(result.LastValid["direct"]) != 3 || result.LastValid["direct"][0].State != "failed" {
		t.Fatal("cancel lost completed results", result)
	}
}

func TestSetupConnectivityReusesMonitorResponsesToInitializeStorage(t *testing.T) {
	c := testConsole(t)
	stubSetupNetwork(t, c)
	var requests atomic.Int32
	const md5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	c.setupNetwork.channel.Publish(catalogTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := ""
		status := http.StatusOK
		header := make(http.Header)
		switch r.URL.Path {
		case "/Api/Songs/list":
			body = string(monitorNames("20261016000000", "Udon must not replace MD5 metadata").Body)
			// Use one ID so the chosen playback sample is deterministic.
			body = strings.ReplaceAll(body, `,{"id":2,"name":"new song"}`, "")
		case "/api/v2/wanna/songs":
			body = string(monitorFull("kiva", "20261016000000", "initialized by monitor", md5).Body)
		case "/api/wannaInfo":
			body = string(monitorFull("wanna", "20261015000000", "older mirror", md5).Body)
		case "/Api/Songs/play":
			status = http.StatusFound
			header.Set("Location", "https://play.udon.dance/files/1/42-video.mp4?e="+md5+"&s=1234")
		default:
			if r.Header.Get("Range") != "bytes=0-0" {
				t.Error("unexpected download", r.URL.Path, r.Header.Get("Range"))
			}
			status, body = http.StatusPartialContent, "x"
			header.Set("Content-Range", "bytes 0-0/1234")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	before, settings := upstreamrequest.Default.Snapshot(), c.settings
	if err := c.controlSetupConnectivity(false, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.setupNetwork.connectivityDone:
	case <-time.After(5 * time.Second):
		t.Fatal("connectivity did not complete")
	}
	c.mu.Lock()
	state := c.setupNetwork.connectivity
	c.mu.Unlock()
	if state.Phase != "completed" || len(state.Results) != 3 || len(state.LastValid["direct"]) != 3 {
		t.Fatal(state)
	}
	for _, row := range state.Results {
		if row.State != "available" || row.LatencyMS == nil {
			t.Fatal("missing successful summary", row)
		}
	}
	// Three lists, both playback nodes, one deduplicated resource host. Storage
	// initialization must not fetch any of those responses a second time.
	if requests.Load() != 6 {
		t.Fatal("unexpected upstream request count", requests.Load())
	}
	db, err := sql.Open("sqlite", filepath.Join(c.settings.StorageDir, "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name, checksum string
	var size int64
	if err := db.QueryRow("SELECT name,md5,byte_size FROM songs JOIN song_media USING(song_id) JOIN media USING(md5) WHERE song_id=1").Scan(&name, &checksum, &size); err != nil || name != "initialized by monitor" || checksum != md5 || size != 1234 {
		t.Fatalf("initial metadata %q %q %d: %v", name, checksum, size, err)
	}
	for _, node := range []string{"cf", "nya"} {
		urls, err := c.service.SongURLs(context.Background(), 1, node)
		if err != nil || len(urls) != 1 {
			t.Fatalf("playback metadata %s: %v %v", node, urls, err)
		}
	}
	for _, dir := range []string{"videos", "tmp"} {
		files, err := os.ReadDir(filepath.Join(c.settings.StorageDir, dir))
		if err != nil && !os.IsNotExist(err) || len(files) != 0 {
			t.Fatal("connectivity retained video data", dir, files, err)
		}
	}
	if after := upstreamrequest.Default.Snapshot(); after.Transport != before.Transport || after.Revision != before.Revision || c.settings != settings {
		t.Fatal("diagnostic changed business routing")
	}
}

func TestSetupConnectivityCanceledCatalogDoesNotInitializeStorage(t *testing.T) {
	c := testConsole(t)
	stubSetupNetwork(t, c)
	entered := make(chan struct{})
	c.setupNetwork.channel.Publish(catalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "wanna.kiva.moe" {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		body := monitorFull("kiva", "20261016000000", "must not be delivered", strings.Repeat("a", 32)).Body
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}))
	if err := c.controlSetupConnectivity(false, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("catalog not requested")
	}
	if err := c.controlSetupConnectivity(true, setupCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	<-c.setupNetwork.connectivityDone
	if c.service != nil {
		t.Fatal("canceled batch initialized storage")
	}
}

func TestSetupConnectivitySummaryAcceptsAnyAvailableCandidate(t *testing.T) {
	ms := 12.0
	rows := []upstreamstate.Result{
		{Operation: upstreamstate.Catalog, State: "failed", Reason: "http_error"},
		{Operation: upstreamstate.Catalog, State: "available", EstimatedLatencyMS: &ms},
		{Operation: upstreamstate.Catalog, State: "failed", Reason: "timeout"},
	}
	row := summarizeConnectivity(upstreamstate.Catalog, rows)
	if row.State != "available" || row.Reason != "" || row.LatencyMS == nil || *row.LatencyMS != ms {
		t.Fatal(row)
	}
}
