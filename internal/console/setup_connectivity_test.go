package console

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
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
