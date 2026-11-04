package console

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestSOCKS5ServiceScheduleAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testConsole(t)
		c.settings.UpstreamMode, c.settings.ProxyBackend = "proxy", "external-socks5"
		c.settings.proxySecretError = "unavailable test secret"
		c.savedSettings = c.settings
		c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
			return automaticConnectivityResponse(r), nil
		})}
		c.mu.Lock()
		if status := c.socks5ServiceSnapshotLocked(); status == nil || !status.ObservedAt.IsZero() {
			t.Fatal(status)
		}
		if c.networkDiagnostics != nil {
			t.Fatal("reading status started diagnostics")
		}
		c.mu.Unlock()
		c.terms = termsReceipt{}
		if err := c.checkSOCKS5Service(); err == nil {
			t.Fatal("manual check accepted before consent")
		}
		if err := c.StartUpstreamServices(); err != nil {
			t.Fatal(err)
		}
		if c.networkDiagnostics != nil {
			t.Fatal("started before consent")
		}
		if err := c.acceptTerms(); err != nil {
			t.Fatal(err)
		}
		if err := c.StartUpstreamServices(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		snapshot := func() *socks5ServiceStatus { c.mu.Lock(); defer c.mu.Unlock(); return c.socks5ServiceSnapshotLocked() }
		first := snapshot()
		if first.State != "failed" || first.Error != unavailableProxySecret || first.NextCheck.Sub(first.ObservedAt) != 30*time.Second {
			t.Fatal(first)
		}
		// Saved settings do not switch the active probe or its credentials.
		c.savedSettings.UpstreamMode = "direct"
		c.savedSettings.proxySecretError = ""
		time.Sleep(29 * time.Second)
		synctest.Wait()
		if !snapshot().ObservedAt.Equal(first.ObservedAt) {
			t.Fatal("early check")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		second := snapshot()
		if !second.ObservedAt.Equal(first.NextCheck) || second.State != "failed" {
			t.Fatal(second)
		}
		time.Sleep(10 * time.Second)
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/network/socks5/check", strings.NewReader("{}"))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		// Repeated triggers join the scheduled check without queuing another.
		if err := c.checkSOCKS5Service(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		manual := snapshot()
		if manual.Checking || manual.ObservedAt.Sub(second.ObservedAt) != 10*time.Second || manual.NextCheck.Sub(manual.ObservedAt) != socks5ServiceInterval {
			t.Fatal(manual)
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if !snapshot().ObservedAt.Equal(manual.ObservedAt) {
			t.Fatal("old automatic deadline was not replaced")
		}
		second = manual
		c.BeginShutdown()
		if err := c.checkSOCKS5Service(); err == nil {
			t.Fatal("manual check accepted after shutdown")
		}
		c.networkDiagnostics.close()
		time.Sleep(time.Minute)
		synctest.Wait()
		last := snapshot()
		if !last.Closed || last.Checking || !last.NextCheck.IsZero() || !last.ObservedAt.Equal(second.ObservedAt) {
			t.Fatal(last)
		}
	})
}

func TestSOCKS5ServiceActiveConfigurationAndStatus(t *testing.T) {
	c := testConsole(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	var first sync.Once
	serve := func(conn net.Conn) {
		defer first.Do(func() { close(closed) })
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		greeting := make([]byte, 4)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			t.Error(err)
			return
		}
		conn.Write([]byte{5, 2})
		auth := make([]byte, 3+len("active-user")+len("active-secret"))
		if _, err := io.ReadFull(conn, auth); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(auth), "active-secret") {
			t.Error("did not use active credentials")
		}
		conn.Write([]byte{1, 0})
		var extra [1]byte
		if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
			t.Error("probe sent CONNECT or did not close", n, err)
		}
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			serve(conn)
		}
	}()
	c.settings.UpstreamMode, c.settings.ProxyBackend = "proxy", "external-socks5"
	c.settings.SOCKS5Address = listener.Addr().String()
	c.settings.SOCKS5Username, c.settings.SOCKS5Password = "active-user", "active-secret"
	c.savedSettings = c.settings
	c.savedSettings.SOCKS5Address = "127.0.0.1:1"
	c.savedSettings.SOCKS5Password = "saved-secret"
	c.mu.Lock()
	n, err := c.networkDiagnosticsLocked()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.checkSOCKS5(); err != nil {
		t.Fatal(err)
	}
	c.mu.Unlock()
	defer func() { c.BeginShutdown(); c.networkDiagnostics.close() }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Fatal("probe did not finish")
	}
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		status := c.socks5ServiceSnapshotLocked()
		c.mu.Unlock()
		if status.State == "available" {
			if !status.Authenticated || status.LatencyMS == nil {
				t.Fatal(status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(status)
		}
		time.Sleep(time.Millisecond)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	r.Header.Set("X-SWD-Token", c.token)
	c.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "active-secret") || strings.Contains(w.Body.String(), "saved-secret") {
		t.Fatal("status leaked password")
	}
	var body struct {
		Service socks5ServiceStatus `json:"socks5Service"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Service.State != "available" {
		t.Fatal(err, body)
	}
	if evidence := os.Getenv("SWD_SOCKS5_BROWSER_EVIDENCE"); evidence != "" {
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		defer ui.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "socks5-service-live.mjs"), ui.URL, evidence)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("browser: %v\n%s", err, output)
		}
	}
}

func TestSOCKS5ServiceOnlyExternalProxy(t *testing.T) {
	for _, mode := range []string{"direct", "proxy"} {
		c := testConsole(t)
		c.settings.UpstreamMode, c.settings.ProxyBackend = mode, "mihomo"
		c.mu.Lock()
		n, err := c.networkDiagnosticsLocked()
		if err != nil {
			t.Fatal(err)
		}
		if err := n.checkSOCKS5(); err == nil || c.socks5ServiceSnapshotLocked() != nil {
			t.Error("started for", mode)
		}
		c.mu.Unlock()
		if err := c.checkSOCKS5Service(); err == nil {
			t.Error("manual check accepted for", mode)
		}
	}
}
