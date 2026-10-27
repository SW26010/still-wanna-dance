package console

import (
	"net/http"
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestDiagnosticDirectScopeDoesNotReplaceBusinessProxy(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
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
	c.requestRevision = upstreamrequest.Default.Publish(proxyClient.Transport.(upstreamrequest.Candidates))
	before := upstreamrequest.Default.Snapshot()
	n, err := c.newNetworkDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if after := upstreamrequest.Default.Snapshot(); after.Candidates != before.Candidates || after.Revision != before.Revision {
		t.Fatal("diagnostic changed business channel")
	}
	if n.channel == upstreamrequest.Default || c.client != proxyClient || c.settings.UpstreamMode != "proxy" || c.savedSettings.UpstreamMode != "proxy" {
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
	if testDNS(c).queries.Load() != 0 {
		t.Fatal("scope construction queried DNS")
	}
}

// Isolated transport: never contacts an upstream, even on consent API tests.
func stubNetworkDiagnostics(t *testing.T, c *Console) <-chan struct{} {
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
	monitor, err := c.newDiagnosticMonitor(channel)
	if err != nil {
		t.Fatal(err)
	}
	c.networkDiagnostics = &networkDiagnostics{monitor: monitor, channel: channel, revision: revision, client: &http.Client{Transport: transport}}
	return started
}
