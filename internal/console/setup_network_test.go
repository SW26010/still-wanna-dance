package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestSetupDirectScopeDoesNotReplaceBusinessProxy(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "socks5"
	c.settings.SOCKS5Address = "127.0.0.1:9"
	c.savedSettings = c.settings
	_, proxyClient, err := c.networkFor(c.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer proxyClient.Transport.(interface{ Retire() }).Retire()
	c.client.CloseIdleConnections()
	c.client.Transport.(interface{ Retire() }).Retire()
	c.client = proxyClient
	c.requestRevision = upstreamrequest.Default.Publish(proxyClient.Transport)
	before := upstreamrequest.Default.Snapshot()
	n, err := c.newSetupNetwork()
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if after := upstreamrequest.Default.Snapshot(); after.Transport != before.Transport || after.Revision != before.Revision {
		t.Fatal("diagnostic changed business channel")
	}
	if n.channel == upstreamrequest.Default || c.client != proxyClient || c.settings.UpstreamMode != "socks5" || c.savedSettings.UpstreamMode != "socks5" {
		t.Fatal("diagnostic changed proxy settings")
	}
	for _, candidate := range before.Candidates.Current("https://api.udon.dance") {
		if candidate.Mode != "socks5" {
			t.Fatal("business has direct fallback")
		}
	}
	// Cold direct DNS is not queried by constructing a scope or reading status.
	if len(n.channel.Snapshot().Candidates.Current("https://api.udon.dance")) != 0 {
		t.Fatal("unexpected cold direct candidate")
	}
	if len(c.dns.cache) != 0 {
		t.Fatal("scope construction queried DNS")
	}
}

func TestSetupNetworkGateSingleFlightAndShutdown(t *testing.T) {
	c := testConsole(t)
	accepted := c.terms
	c.terms = termsReceipt{}
	if err := c.checkSetupNetwork(); err == nil || c.setupNetwork != nil {
		t.Fatal("diagnostics bypassed consent")
	}
	c.terms = accepted
	started := stubSetupNetwork(t, c)
	for _, tc := range []struct {
		token, origin string
		want          int
	}{{"", "", 403}, {c.token, "https://example.invalid", 403}, {c.token, "", 200}} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/network", strings.NewReader("{}"))
		r.Header.Set("X-SWD-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("network auth: %d", w.Code)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("diagnostic never started")
	}
	done := c.setupNetwork.done
	if err := c.checkSetupNetwork(); err != nil || c.setupNetwork.done != done {
		t.Fatal("duplicate diagnostic task", err)
	}
	// Reading progress and leaving the page have no cancellation side effect.
	_, _ = c.setupProgress()
	select {
	case <-done:
		t.Fatal("page read stopped diagnostic")
	default:
	}
	c.BeginShutdown()
	closed := make(chan struct{})
	go func() { c.setupNetwork.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel diagnostic")
	}
	if err := c.checkSetupNetwork(); err == nil {
		t.Fatal("started during shutdown")
	}
	if err := c.setupNetwork.monitor.Check(context.Background()); err == nil {
		t.Fatal("closed monitor still accepts work")
	}
}

func TestSetupSelectedNetworkRoutesKeepConsentAndDiagnosticScope(t *testing.T) {
	for _, kind := range []upstreamstate.CheckKind{upstreamstate.CheckCatalog, upstreamstate.CheckPlayback, upstreamstate.CheckThroughput} {
		t.Run(string(kind), func(t *testing.T) {
			c := testConsole(t)
			started := stubSetupNetwork(t, c)
			before := upstreamrequest.Default.Snapshot()
			request := func(token string) int {
				r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/network/"+string(kind), strings.NewReader("{}"))
				r.Header.Set("X-SWD-Token", token)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				return w.Code
			}
			if status := request(""); status != 403 {
				t.Fatalf("missing token: %d", status)
			}
			accepted := c.terms
			c.terms = termsReceipt{}
			if status := request(c.token); status != 428 {
				t.Fatalf("missing consent: %d", status)
			}
			c.terms = accepted
			if status := request(c.token); status != 200 {
				t.Fatalf("selected check: %d", status)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("selected diagnostic never started")
			}
			if got := c.setupNetwork.monitor.Snapshot().CheckKind; got != kind {
				t.Fatalf("kind %q, want %q", got, kind)
			}
			if after := upstreamrequest.Default.Snapshot(); after.Transport != before.Transport || after.Revision != before.Revision {
				t.Fatal("selected diagnostic changed business channel")
			}
			c.BeginShutdown()
		})
	}
}

// Isolated transport: never contacts an upstream, even on consent API tests.
func stubSetupNetwork(t *testing.T, c *Console) <-chan struct{} {
	t.Helper()
	started := make(chan struct{}, 16)
	transport := catalogTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	channel := upstreamrequest.NewChannel()
	revision := channel.Publish(transport)
	monitor, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	c.setupNetwork = &setupNetwork{monitor: monitor, channel: channel, revision: revision, client: &http.Client{Transport: transport}}
	return started
}
