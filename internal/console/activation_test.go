package console

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"stepstash/internal/cacheproxy"
)

func TestActivationRecordsPlaybackArrivalOnResolutionFailure(t *testing.T) {
	c := testConsole(t)
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = c.settings.StorageDir
	cfg.BeginVideoRequest = c.beginVideoRequest
	cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
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
		func(string) error { t.Fatal("unnecessary hosts change"); return nil }); err != nil {
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
	change := func(string) error { calls++; return errors.New("UAC canceled") }
	if c.enableAcceleration(inspect, change) == nil || c.DesktopState().CDN || calls != 0 {
		t.Fatal("conflict must fail before startup/elevation")
	}
	hosts = HostsStatus{}
	if c.enableAcceleration(inspect, change) == nil || !c.DesktopState().CDN || c.activation.Phase != "failed" {
		t.Fatal("UAC refusal must preserve running service and report failure")
	}
	server := c.httpServer
	if c.enableAcceleration(inspect, func(string) error { return nil }) == nil {
		t.Fatal("helper success without ready hosts must fail")
	}
	hosts.Ready = true
	if err := c.enableAcceleration(inspect, change); err != nil || calls != 1 || c.httpServer != server {
		t.Fatal("retry must reuse running service and avoid unnecessary elevation", err)
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
	if err := c.enableAcceleration(inspect, change); err != nil {
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

func TestActivationPortFailureNeverChangesHosts(t *testing.T) {
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
			err = c.enableAcceleration(func() HostsStatus { return HostsStatus{} }, func(string) error { t.Fatal("elevation after bind failure"); return nil })
			if err == nil || c.DesktopState().CDN || c.activation.Phase != "failed" {
				t.Fatal("port conflict not reported", err)
			}
		})
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
	close(release)
	if finish := <-done; finish != nil || err == nil || phase != "hosts" {
		t.Fatal(phase, err, finish)
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
