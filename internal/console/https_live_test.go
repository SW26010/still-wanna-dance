package console

import (
	"context"
	"net"
	"os"
	"still-wanna-dance/internal/upstreamrequest"
	"testing"
	"time"
)

// Opt-in browser acceptance helper; never runs in normal tests or CI. It uses
// only port 443 and the production DNS/relay, without touching hosts or cache.
func TestHTTPSLiveBrowser(t *testing.T) {
	if os.Getenv("STEPSTASH_HTTPS_LIVE") != "1" {
		t.Skip("set STEPSTASH_HTTPS_LIVE=1 for a two-minute Chrome acceptance session")
	}
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	for _, route := range []string{"cf", "hkg"} {
		if _, err := c.resolveNode(ctx, 1343, route); err != nil {
			t.Logf("API %s: %v", route, err)
		}
	}
	if len(upstreamrequest.Default.ResourceDomains()) == 0 {
		t.Fatal("API returned no valid resource domains")
	}
	t.Logf("API-confirmed resource domains: %v", upstreamrequest.Default.ResourceDomains())
	l, err := net.Listen("tcp4", "127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	dns := &directDNS{}
	p := newHTTPSRelay(l, func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dns.DialContext(ctx, network, address)
		if err == nil {
			t.Logf("upstream %s -> %s", address, conn.RemoteAddr())
		}
		return conn, err
	})
	done := make(chan error, 1)
	go func() { done <- p.serve() }()
	defer func() { p.close(); <-done }()
	t.Log("HTTPS browser acceptance ready at 127.0.0.1:443 for two minutes")
	<-time.After(2 * time.Minute)
}
