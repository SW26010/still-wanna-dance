package console

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestNetworkCalibrationConsentAuthAndIsolation(t *testing.T) {
	c := testConsole(t)
	stubNetworkDiagnostics(t, c)
	accepted := c.terms
	request := func(method, path, token, origin string) int {
		r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader("{}"))
		r.Header.Set("X-SWD-Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w.Code
	}
	start := "/api/network/calibration/start"
	if code := request("POST", start, "", ""); code != 403 {
		t.Fatalf("missing token: %d", code)
	}
	if code := request("POST", start, c.token, "https://example.invalid"); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	c.terms = termsReceipt{}
	for _, path := range []string{start, "/api/network/calibration/cancel"} {
		if code := request("POST", path, c.token, ""); code != 428 {
			t.Fatalf("missing terms: %d", code)
		}
	}
	if code := request("GET", "/api/network/calibration/events", "", ""); code != 428 {
		t.Fatalf("event consent: %d", code)
	}
	c.terms = accepted
	if code := request("GET", "/api/network/calibration/events", "", "https://example.invalid"); code != 403 {
		t.Fatalf("event origin: %d", code)
	}
	beforeSettings, beforeClient := c.settings, c.client
	c.networkDiagnostics.connectivity.Running = true
	if err := c.controlNetworkCalibration(false, networkCalibrationRequest{}); err == nil {
		t.Fatal("overlapped direct diagnostic")
	}
	c.networkDiagnostics.connectivity.Running = false
	if code := request("POST", start, c.token, ""); code != 200 {
		t.Fatalf("start: %d", code)
	}
	if err := c.controlNetworkConnectivity(false, networkCalibrationRequest{}); err == nil {
		t.Fatal("overlapped calibration")
	}
	if code := request("POST", "/api/network/calibration/cancel", c.token, ""); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	c.networkDiagnostics.calibration.Close()
	if c.settings != beforeSettings || c.client != beforeClient {
		t.Fatal("calibration modified business state")
	}
}

func TestNetworkCalibrationSOCKS5DraftAndCancellation(t *testing.T) {
	c := testConsole(t)
	const user, password = "fixture-user", "fixture-private-password"
	connected := make(chan string, 4)
	disconnected := make(chan struct{}, 4)
	address := socksPeerWithAuth(t, user, password, func(conn net.Conn, target string) {
		connected <- target
		socksReply(conn, 0)
		_, _ = io.Copy(io.Discard, conn)
		disconnected <- struct{}{}
	})
	// Empty password in the form retains the saved credential, while the
	// unsaved endpoint is used for this test only.
	c.savedSettings.SOCKS5Username, c.savedSettings.SOCKS5Password = user, password
	before, _ := os.ReadFile(c.configPath)
	saved, active, client := c.savedSettings, c.settings, c.client
	business := upstreamrequest.Default.Snapshot()
	input := networkCalibrationRequest{Upstream: "proxy", Proxy: &diagnosticProxyDraft{
		ProxyBackend: "external-socks5", SOCKS5Address: address, SOCKS5User: user}}
	body, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/network/calibration/start", bytes.NewReader(body))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("proxy start: %d %s", w.Code, w.Body.String())
	}
	select {
	case target := <-connected:
		if !strings.HasSuffix(target, ":443") {
			t.Fatalf("unexpected remote DNS target: %s", target)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("draft SOCKS5 was not used")
	}
	test := c.networkDiagnostics.calibration
	state, _ := test.Snapshot()
	if state.Upstream != "proxy" || !state.Running || state.Run != 1 {
		t.Fatalf("incorrect upstream event: %+v", state)
	}
	if err := c.controlNetworkCalibration(false, input); err != nil {
		t.Fatal(err)
	}
	if err := c.controlNetworkCalibration(false, networkCalibrationRequest{}); err == nil {
		t.Fatal("changed upstream during active run")
	}
	if err := c.controlNetworkCalibration(true, networkCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	test.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("cancellation retained proxy connection")
	}
	state, _ = test.Snapshot()
	encoded, _ := json.Marshal(state)
	if state.Upstream != "proxy" || state.Phase != "canceled" || state.Run != 1 || strings.Contains(string(encoded), password) || strings.Contains(string(encoded), address) {
		t.Fatal("canceled result lost upstream or exposed proxy configuration")
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(before, after) || saved != c.savedSettings || active != c.settings || client != c.client || upstreamrequest.Default.Snapshot().Revision != business.Revision || testDNS(c).queries.Load() != 0 {
		t.Fatal("test changed business configuration/channel or queried direct DNS")
	}
}

func TestNetworkCalibrationRejectsInvalidProxyBeforeStarting(t *testing.T) {
	c := testConsole(t)
	for _, input := range []networkCalibrationRequest{
		{Upstream: "auto"}, {Upstream: "proxy"},
		{Upstream: "proxy", Proxy: &diagnosticProxyDraft{ProxyBackend: "mihomo"}},
		{Upstream: "proxy", Proxy: &diagnosticProxyDraft{SOCKS5Address: "invalid"}},
	} {
		if err := c.controlNetworkCalibration(false, input); err == nil {
			t.Fatal("invalid proxy accepted")
		}
		if c.networkDiagnostics != nil {
			t.Fatal("invalid proxy constructed diagnostic")
		}
	}
}

func TestNetworkCalibrationEventReconnectAndShutdown(t *testing.T) {
	c := testConsole(t)
	started := stubNetworkDiagnostics(t, c)
	server := httptest.NewServer(c)
	defer server.Close()
	c.address = strings.TrimPrefix(server.URL, "http://")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func() (*http.Response, *bufio.Scanner) {
		r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/network/calibration/events", nil)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("bad event response: %v", resp)
		}
		return resp, bufio.NewScanner(resp.Body)
	}
	read := func(scan *bufio.Scanner) upstreamstate.CalibrationStatus {
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "data: ") {
				var state upstreamstate.CalibrationStatus
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &state); err != nil {
					t.Fatal(err)
				}
				return state
			}
		}
		t.Fatalf("stream ended before event: %v", scan.Err())
		return upstreamstate.CalibrationStatus{}
	}
	resp, scan := open()
	defer resp.Body.Close()
	if s := read(scan); s.Phase != "idle" || s.Running {
		t.Fatalf("subscription started work: %+v", s)
	}
	select {
	case <-started:
		t.Fatal("read performed network request")
	default:
	}
	if err := c.controlNetworkCalibration(false, networkCalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	s := read(scan)
	if !s.Running || s.Run != 1 {
		t.Fatalf("no start event: %+v", s)
	}
	resp.Body.Close()
	resp2, scan2 := open()
	defer resp2.Body.Close()
	if s := read(scan2); !s.Running || s.Run != 1 {
		t.Fatalf("reconnect lost/restarted task: %+v", s)
	}
	c.BeginShutdown()
	if s := read(scan2); !s.Closed {
		t.Fatalf("shutdown did not end stream: %+v", s)
	}
	c.networkDiagnostics.calibration.Close()
}
