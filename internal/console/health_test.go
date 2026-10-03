package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const healthCatalog = `{"groups":{"contents":[{"songInfos":[{"id":1,"name":"test"}]}]}}`

func TestHealthClassifiesResponses(t *testing.T) {
	for _, tc := range []struct {
		name, id, body, state string
		status                int
	}{
		{"edge", "api", "ok", "reachable", 200},
		{"catalog", "catalog", healthCatalog, "healthy", 200},
		{"bad catalog", "catalog", "<html>challenge</html>", "invalid", 200},
		{"origin timeout", "catalog", "", "upstream_error", 524},
		{"server error", "api", "", "upstream_error", 503},
		{"denied", "catalog", "", "http_error", 403},
		{"no redirect follow", "catalog", "", "http_error", 302},
		{"hkg root missing", "hkg", "", "reachable", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.id == "hkg" && r.Method != "HEAD" {
					t.Error("video entry probe must not download a video")
				}
				w.Header().Set("Location", "https://must-not-follow.invalid/")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			c := testConsole(t)
			_, client, err := c.networkFor(c.settings)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			got := probeHealth(context.Background(), client, HealthCheck{ID: tc.id, URL: s.URL})
			if got.State != tc.state || got.HTTP != tc.status || got.Checked.IsZero() {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestHealthTimeoutStages(t *testing.T) {
	for _, body := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body {
				fmt.Fprint(w, "{")
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		}))
		client := s.Client()
		client.Timeout = 50 * time.Millisecond
		got := probeHealth(context.Background(), client, HealthCheck{ID: "catalog", URL: s.URL})
		s.Close()
		stage := "headers"
		if body {
			stage = "body"
		}
		if got.State != "timeout" || got.Stage != stage || !strings.Contains(got.Message, "无法区分") {
			t.Fatalf("%+v", got)
		}
	}
}

func TestHealthUsesAuthenticatedSOCKSAndRemoteDNS(t *testing.T) {
	requested := make(chan string, 1)
	address := socksPeerWithAuth(t, "probe-user", "probe-secret", func(conn net.Conn, target string) {
		requested <- target
		socksReply(conn, 0)
		r, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		r.Body.Close()
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(healthCatalog), healthCatalog)
	})
	c := testConsole(t)
	s := c.settings
	s.UpstreamMode, s.SOCKS5Address = "socks5", address
	s.SOCKS5Username, s.SOCKS5Password = "probe-user", "probe-secret"
	_, client, err := c.networkFor(s)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	got := probeHealth(context.Background(), client, HealthCheck{ID: "catalog", URL: "http://health-only.invalid/Api/Songs/list"})
	if got.State != "healthy" {
		t.Fatalf("%+v", got)
	}
	if target := <-requested; target != "health-only.invalid:80" {
		t.Fatal(target)
	}
	client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("proxy failure with probe-secret")
	})
	got = probeHealth(context.Background(), client, HealthCheck{ID: "api", URL: "https://health-only.invalid/"})
	b, _ := json.Marshal(got)
	if got.State != "network_error" || strings.Contains(string(b), "probe-secret") {
		t.Fatal(string(b))
	}
}

func TestHealthSingleFlightSwitchAndSchedule(t *testing.T) {
	c := testConsole(t)
	entered := make(chan struct{}, 4)
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-r.Context().Done()
		// Simulate a transport returning a stale success after cancellation.
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(healthCatalog))}, nil
	})}
	done := make(chan struct{})
	go func() { c.runHealthCheck(context.Background()); close(done) }()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("probe did not start")
		}
	}
	// A second run must not issue additional network requests.
	c.runHealthCheck(context.Background())
	c.mu.Lock()
	s := c.settings
	c.mu.Unlock()
	s.UpstreamMode, s.SOCKS5Address = "socks5", "127.0.0.1:12345"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("network switch did not cancel")
	}
	c.mu.Lock()
	h := c.healthSnapshotLocked()
	c.mu.Unlock()
	if len(h.Checks) != 0 || h.Running || h.Mode != "socks5" {
		t.Fatalf("stale results: %+v", h)
	}
	var calls atomic.Int32
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(healthCatalog))}, nil
	})}
	c.runHealthCheck(context.Background())
	c.runHealthCheck(context.Background())
	if calls.Load() != 4 {
		t.Fatal("automatic check ignored interval", calls.Load())
	}
	c.mu.Lock()
	h = c.healthSnapshotLocked()
	h.Checks[0].State = "modified"
	unchanged := c.health.Checks[0].State != "modified"
	c.mu.Unlock()
	if !unchanged || h.NextCheck.Sub(h.Finished) < healthInterval {
		t.Fatal("bad snapshot or schedule")
	}
}

func TestHealthMonitorCloseCancelsAndJoins(t *testing.T) {
	c := testConsole(t)
	entered := make(chan struct{}, 4)
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	c.StartUpstreamMonitor()
	c.StartUpstreamMonitor()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("probe did not start")
		}
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close blocked")
	}
	select {
	case <-c.healthDone:
	default:
		t.Fatal("worker still running")
	}
}

func TestHealthCheckAPIRequiresToken(t *testing.T) {
	c := testConsole(t)
	for _, token := range []string{"", c.token} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/health/check", nil)
		r.Header.Set("X-StepStash-Token", token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		want := 403
		if token != "" {
			want = 200
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestHealthConsentAndManualRefresh(t *testing.T) {
	c := testConsole(t)
	accepted := c.terms
	c.terms = termsReceipt{}
	var calls atomic.Int32
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(healthCatalog))}, nil
	})}
	c.StartUpstreamMonitor()
	// An authenticated action still cannot bypass the terms gate.
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/health/check", nil)
	r.Header.Set("X-StepStash-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 428 || calls.Load() != 0 {
		t.Fatal("terms bypass", w.Code, calls.Load())
	}
	c.mu.Lock()
	c.terms = accepted
	c.wakeHealthLocked()
	c.mu.Unlock()
	wait := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			c.mu.Lock()
			ready := !c.health.Running && calls.Load() == want
			c.mu.Unlock()
			if ready {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("monitor did not finish", calls.Load())
	}
	wait(4)
	if err := c.requestHealthCheck(); err == nil {
		t.Fatal("manual cooldown should explain why no new probe started")
	}
	c.mu.Lock()
	if c.health.NextCheck.IsZero() {
		t.Error("manual cooldown ignored")
	}
	c.health.Started = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	if err := c.requestHealthCheck(); err != nil {
		t.Fatal(err)
	}
	wait(8)
}
