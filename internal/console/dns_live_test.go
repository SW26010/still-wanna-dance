package console

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"still-wanna-dance/internal/applog"
)

// Requires working public IPv4 and IPv6. Only reads a small Range, never changes
// hosts, and never logs the signed resource URL.
func TestLiveDirectDualStack(t *testing.T) {
	if os.Getenv("SWD_LIVE_DUALSTACK") != "1" {
		t.Skip("opt-in live dual-stack check")
	}
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target, err := c.resolveSongURL(ctx, 1343)
	if err != nil {
		t.Fatal(applog.SafeError(err))
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(applog.SafeError(err))
	}
	u.Scheme = "https"
	for _, host := range []string{"api.udon.dance", u.Hostname()} {
		ips, err := c.dns.Addresses(ctx, host)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("DNS %s: %v", host, ips)
	}
	for _, mode := range []string{"tcp6", "tcp4", "tcp", "relay"} {
		t.Run(mode, func(t *testing.T) {
			network := mode
			if mode == "relay" {
				network = "tcp"
			}
			dial := func(ctx context.Context, _ string, address string) (net.Conn, error) {
				conn, err := c.dns.DialContext(ctx, network, address)
				if err == nil {
					t.Logf("connected %s -> %s", address, conn.RemoteAddr())
				}
				return conn, err
			}
			if mode == "relay" {
				relay := runRelay(t, dial)
				dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, relay.listener.Addr().String())
				}
			}
			tr := &http.Transport{DialContext: dial, TLSHandshakeTimeout: 10 * time.Second}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
			req.Header.Set("Range", "bytes=0-1023")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(applog.SafeError(err))
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
			if err != nil || resp.StatusCode != 206 || len(body) != 1024 || resp.TLS == nil || len(resp.TLS.VerifiedChains) == 0 {
				t.Fatalf("status=%d bytes=%d TLS=%v error=%v", resp.StatusCode, len(body), resp.TLS != nil, err)
			}
			t.Log("certificate-verified HTTPS Range: 206, 1024 bytes")
		})
	}
}
