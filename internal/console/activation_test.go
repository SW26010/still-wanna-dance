package console

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestActivationRecordsPlaybackArrivalOnResolutionFailure(t *testing.T) {
	c := testConsole(t)
	cfg := fixtureCacheConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.BeginVideoRequest = c.beginVideoRequest
	cfg.ResolvePlayback = func(context.Context, string) (string, error) {
		c.mu.Lock()
		received := !c.activation.FirstRequest.IsZero()
		c.mu.Unlock()
		if !received {
			t.Error("activation still waiting when upstream resolution begins")
		}
		return "", errors.New("isolated upstream unavailable")
	}
	var err error
	c.service, err = cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.enableAcceleration(func() HostsStatus { return HostsStatus{Ready: true} },
		func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c.service.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=1", nil))
	if w.Code != 502 || c.activation.FirstRequest.IsZero() {
		t.Fatalf("resolution status=%d, arrival=%v", w.Code, c.activation.FirstRequest)
	}
}

func TestActivationFailuresAndRetry(t *testing.T) {
	c := testConsole(t)
	hosts := HostsStatus{Conflict: true, Message: "conflict"}
	inspect := func() HostsStatus { return hosts }
	calls := 0
	change := func(action string) error {
		if action == "rollback" {
			return nil
		}
		calls++
		return errors.New("UAC canceled")
	}
	if c.enableAcceleration(inspect, change) == nil || c.DesktopState().CDN || calls != 0 {
		t.Fatal("conflict must fail before startup/elevation")
	}
	if !strings.Contains(c.activation.ConnectionError, "conflict") || c.cdnError != "" {
		t.Fatal("hosts conflict was not scoped to the connection", c.activation, c.cdnError)
	}
	hosts = HostsStatus{}
	if c.enableAcceleration(inspect, change) == nil || c.DesktopState().CDN || c.activation.Phase != "failed" {
		t.Fatal("UAC refusal must leave the stopped service unchanged")
	}
	if !strings.Contains(c.activation.ConnectionError, "UAC canceled") || c.cdnError != "" {
		t.Fatal("UAC failure was not scoped to the connection", c.activation, c.cdnError)
	}
	request := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	request.Header.Set("X-SWD-Token", c.token)
	response := httptest.NewRecorder()
	c.ServeHTTP(response, request)
	var status struct {
		Activation map[string]any `json:"activation"`
		CDNError   string         `json:"cdnError"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != 200 ||
		status.Activation["connectionError"] != c.activation.ConnectionError || status.CDNError != "" {
		t.Fatal("status API lost the scoped connection reason", response.Code, err, status)
	}
	if c.enableAcceleration(inspect, func(string) error { return nil }) == nil {
		t.Fatal("helper success without ready hosts must fail")
	}
	if !strings.Contains(c.activation.ConnectionError, "hosts 接入检查未通过") {
		t.Fatal("verification failure has no connection reason", c.activation)
	}
	hosts.Ready = true
	if err := c.enableAcceleration(inspect, func(string) error { return nil }); err != nil || calls != 1 || c.httpServer == nil {
		t.Fatal("retry did not start service", err)
	}
	if c.activation.ConnectionError != "" || c.activation.Error != "" {
		t.Fatal("retry retained connection failure", c.activation)
	}
	old := c.activation.ReadyAt.Add(-time.Nanosecond)
	c.beginVideoRequest(old)()
	if !c.activation.FirstRequest.IsZero() {
		t.Fatal("old in-flight request counted")
	}
	c.beginVideoRequest(time.Now())()
	first := c.activation.FirstRequest
	c.beginVideoRequest(time.Now())()
	if first.IsZero() || !c.activation.FirstRequest.Equal(first) {
		t.Fatal("first arrival not retained")
	}
	late := c.beginVideoRequest(time.Now())
	if err := c.enableAcceleration(inspect, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	late()
	if !c.activation.FirstRequest.IsZero() {
		t.Fatal("retry retained old observation")
	}
	c.beginVideoRequest(time.Now())()
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	c.beginVideoRequest(time.Now())()
	if c.activation.Phase != "stopped" || !c.activation.FirstRequest.IsZero() {
		t.Fatal("stop did not end detection")
	}
}

func TestActivationPortFailureRollsBackHosts(t *testing.T) {
	for _, https := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[https], func(t *testing.T) {
			c := testConsole(t)
			l, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if https {
				c.httpsAddress = l.Addr().String()
			} else {
				c.videoAddress = l.Addr().String()
			}
			ready := false
			var actions []string
			err = c.enableAcceleration(func() HostsStatus { return HostsStatus{Ready: ready} }, func(action string) error {
				actions = append(actions, action)
				ready = action == "enable"
				return nil
			})
			if err == nil || c.DesktopState().CDN || c.activation.Phase != "failed" || ready || strings.Join(actions, ",") != "enable,rollback" {
				t.Fatal("port conflict not reported", err)
			}
			if c.cdnError == "" || c.activation.ConnectionError != "" {
				t.Fatal("port conflict was attributed to hosts", c.activation, c.cdnError)
			}
			// This unrelated listener belongs to the same process. Only the
			// gateway's own listeners may be treated as available, not every PID match.
			w := httptest.NewRecorder()
			c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
			var status struct {
				Running, PortOK, HTTPSPortOK bool
				CDNError                     string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 ||
				status.Running || status.PortOK != https || status.HTTPSPortOK == https || status.CDNError == "" {
				t.Fatalf("real port conflict was hidden: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestActivationSeparatesPortAndRollbackFailures(t *testing.T) {
	c := testConsole(t)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.videoAddress = occupied.Addr().String()
	ready := false
	err = c.enableAcceleration(func() HostsStatus { return HostsStatus{Ready: ready} }, func(action string) error {
		if action == "rollback" {
			return errors.New("fixture restore denied")
		}
		ready = true
		return nil
	})
	if err == nil || c.cdnError == "" || strings.Contains(c.cdnError, "fixture restore denied") ||
		c.activation.ConnectionError != "未能撤销本次游戏接入：fixture restore denied" ||
		!strings.Contains(c.activation.Error, c.cdnError) || !strings.Contains(c.activation.Error, c.activation.ConnectionError) {
		t.Fatal("combined failure lost its component ownership", c.activation, c.cdnError, err)
	}
}

func TestActivationDuplicateAndProgress(t *testing.T) {
	c := testConsole(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	ready := false
	inspect := func() HostsStatus { return HostsStatus{Ready: ready} }
	go func() {
		done <- c.enableAcceleration(inspect, func(string) error {
			close(entered)
			<-release
			ready = true
			return nil
		})
	}()
	<-entered
	c.mu.Lock()
	phase := c.activation.Phase
	c.mu.Unlock()
	beforeReady := c.beginVideoRequest(time.Now())
	err := c.enableAcceleration(func() HostsStatus { t.Error("duplicate ran"); return HostsStatus{} }, nil)
	stopErr := c.disableAcceleration()
	close(release)
	if finish := <-done; finish != nil || err == nil || stopErr == nil || phase != "hosts" {
		t.Fatal(phase, err, stopErr, finish)
	}
	beforeReady()
	if !c.activation.FirstRequest.IsZero() {
		t.Fatal("request arriving during setup counted")
	}
	c.beginVideoRequest(time.Now())()
	c.failCDN(c.httpServer, errors.New("listener failed"))
	if c.activation.Phase != "stopped" || !c.activation.FirstRequest.IsZero() {
		t.Fatal("listener failure retained evidence")
	}
}

func TestActivationEndpointUsesExistingAuthorization(t *testing.T) {
	c := testConsole(t)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("POST", "http://"+c.address+"/api/activation/enable", nil))
	if w.Code != 403 || !c.activation.Started.IsZero() {
		t.Fatal("unauthenticated activation", w.Code)
	}
}

func TestAutoAccelerationUsesOwnedHosts(t *testing.T) {
	c := testConsole(t)
	c.settings.AutoAccelerate = true
	data := "\r\n127.0.0.1 api.udon.dance " + marker + "\r\n"
	c.hosts, _ = fixtureHostsController(t, data)

	c.AutoStart()
	if !c.DesktopState().CDN || c.activation.Phase != "waiting" || !c.hosts.status().Ready {
		t.Fatal(c.activation)
	}
}
