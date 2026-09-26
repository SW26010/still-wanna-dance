package console

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// Opt-in network acceptance: no hosts changes and no full video downloads.
func TestLiveHTTPSUpstreams(t *testing.T) {
	if os.Getenv("STEPSTASH_LIVE_UPSTREAM") != "1" {
		t.Skip("opt-in live HTTPS probe")
	}
	c := testConsole(t)
	for _, route := range []string{"cf", "hkg"} {
		t.Run(route, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			target, err := c.resolveNode(ctx, 1343, route)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(target)
			if err != nil {
				t.Fatal(err)
			}
			u.Scheme = "https"
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				return c.dns.DialContext(ctx, network, httpsOrigin(host))
			}, TLSHandshakeTimeout: 10 * time.Second}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 5 {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Host != "play.udon.dance" && req.URL.Host != "nya.xin.moe" {
					return fmt.Errorf("unsupported redirect")
				}
				if req.URL.Query().Get("e") != u.Query().Get("e") || req.URL.Query().Get("s") != u.Query().Get("s") {
					return fmt.Errorf("changed resource")
				}
				req.URL.Scheme = "https"
				return nil
			}}
			req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
			req.Header.Set("Range", "bytes=0-1023")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 206 {
				redirect, _ := url.Parse(resp.Header.Get("Location"))
				t.Fatalf("HTTPS Range status %d redirect scheme=%s host=%s path=%s", resp.StatusCode, redirect.Scheme, redirect.Host, redirect.Path)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
			if err != nil || len(body) != 1024 {
				t.Fatalf("bytes=%d error=%v", len(body), err)
			}
			t.Logf("%s HTTPS API + certificate-verified video Range passed via built-in DNS", route)
		})
	}
}
