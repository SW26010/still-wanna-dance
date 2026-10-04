package console

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type monitorTestChannels struct{ http.RoundTripper }

func (p monitorTestChannels) Changed() <-chan struct{} { return nil }
func (p monitorTestChannels) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return p.Current(target), nil
}
func (p monitorTestChannels) Current(target string) []upstreamrequest.Candidate {
	u, _ := url.Parse(target)
	return []upstreamrequest.Candidate{
		{ID: "direct/" + u.Host, Host: u.Host, Mode: "direct", IP: "192.0.2.1", Transport: p.RoundTripper},
		{ID: "socks5/" + u.Host, Host: u.Host, Mode: "socks5", Transport: p.RoundTripper},
	}
}

func waitMonitor(t *testing.T, c *Console) upstreamstate.Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		s := c.monitorSnapshotLocked()
		c.mu.Unlock()
		if !s.Checking && !s.Finished.IsZero() {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("monitor did not finish")
	return upstreamstate.Status{}
}

func TestMonitorLifecycleConsentAndStatus(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{}
	var calls atomic.Int32
	transport := catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		// Ensure a measurable elapsed duration on Windows' coarse clock.
		time.Sleep(time.Millisecond)
		response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
		switch r.URL.Path {
		case "/Api/Songs/list":
			response.Body = io.NopCloser(strings.NewReader(`{"groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))
		case "/Api/Songs/play":
			if r.URL.Query().Get("id") != "42" {
				t.Error("missing sample song")
			}
			host := "play.udon.dance"
			if r.URL.Query().Get("node") == "nya" {
				host = "nya.xin.moe"
			}
			response.StatusCode = 302
			response.Header.Set("Location", "https://"+host+"/files/123/42-abc.mp4?e=0123456789abcdef0123456789abcdef&s=131072")
		default:
			if r.Header.Get("Range") != "bytes=0-131071" {
				t.Error("unbounded resource request")
			}
			response.StatusCode = 206
			response.Header.Set("Content-Range", "bytes 0-131071/131072")
			response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 131072)))
		}
		return response, nil
	})
	c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{transport})
	if err := c.StartUpstreamMonitor(); err != nil {
		t.Fatal(err)
	}
	if c.monitor != nil || calls.Load() != 0 {
		t.Fatal("probed before consent")
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/upstream/check", nil)
	r.Header.Set("X-StepStash-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 428 {
		t.Fatal("terms bypass", w.Code)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	m := c.monitor
	if err := c.StartUpstreamMonitor(); err != nil || c.monitor != m {
		t.Fatal("not idempotent", err)
	}
	s := waitMonitor(t, c)
	if !s.Scheduled || len(s.Results) != 10 || calls.Load() != 10 {
		t.Fatalf("bad snapshot: %+v, calls %d", s, calls.Load())
	}
	for _, result := range s.Results {
		if result.State != "available" {
			t.Fatalf("%+v", result)
		}
		if result.Operation == upstreamstate.Resource && (result.EstimatedSpeedBPS == nil || result.SampleSongID != 42) {
			t.Fatalf("missing resource measurement: %+v", result)
		}
	}
	before := calls.Load()
	r = httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	w = httptest.NewRecorder()
	c.ServeHTTP(w, r)
	var status struct {
		Monitor upstreamstate.Status `json:"upstreamMonitor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Monitor.Results) != 10 || calls.Load() != before || strings.Contains(w.Body.String(), "upstreamHealth") {
		t.Fatal("status not a pure canonical snapshot", w.Body.String())
	}
	if err := c.requestMonitorCheck(); err == nil {
		t.Fatal("missing cooldown")
	}
	r = httptest.NewRequest("POST", "http://"+c.address+"/api/upstream/check", nil)
	w = httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing token gate", w.Code)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !m.Snapshot().Closed {
		t.Fatal("monitor not closed")
	}
}

func TestMonitorConfigChangeCancelsAndRestarts(t *testing.T) {
	c := testConsole(t)
	entered := make(chan struct{}, 1)
	c.requestRevision = upstreamrequest.Default.Publish(catalogTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	if err := c.StartUpstreamMonitor(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("did not start")
	}
	c.requestRevision = upstreamrequest.Default.Publish(catalogTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	s := waitMonitor(t, c)
	if s.Results[0].HTTP != 503 {
		t.Fatalf("old configuration leaked: %+v", s)
	}
	if err := c.monitor.SetPolicy(upstreamstate.Policy{Interval: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !c.monitor.Snapshot().Finished.After(s.Finished) {
		if time.Now().After(deadline) {
			t.Fatal("periodic check did not run")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManualMonitorCheckCanceledOnClose(t *testing.T) {
	c := testConsole(t)
	entered := make(chan struct{}, 1)
	c.requestRevision = upstreamrequest.Default.Publish(catalogTransport(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	var err error
	c.monitor, err = upstreamstate.NewMonitor(upstreamstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/upstream/check", nil)
	r.Header.Set("X-StepStash-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("manual check did not start")
	}
	if err := c.requestMonitorCheck(); err != nil {
		t.Fatal("duplicate should join", err)
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel manual check")
	}
	if c.monitorManual || !c.monitor.Snapshot().Closed {
		t.Fatal("manual worker survived close")
	}
	select {
	case <-c.monitorManualDone:
	default:
		t.Fatal("manual worker not joined")
	}
}
