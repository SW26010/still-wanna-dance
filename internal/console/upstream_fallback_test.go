package console

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Like a cold DNS pool, candidates appear only after discovery. Discovery does
// not make an HTTP request or supply a Monitor measurement.
type coldBusinessChannels struct {
	http.RoundTripper
	channels []upstreamrequest.Candidate
	ready    bool
}

// The real Pool sees an empty DNS cache until it requests address discovery.
type coldCandidateDNS struct {
	*fixtureDNS
	answers []string
}

func (d *coldCandidateDNS) Addresses(ctx context.Context, host string) ([]upstreamrequest.Address, error) {
	d.queries.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.seed(host, d.answers...)
	return d.CurrentAddresses(host), nil
}

func TestUnmeasuredOperationClientsRespectModes(t *testing.T) {
	for _, cache := range []string{"cold", "warm"} {
		t.Run(cache, func(t *testing.T) {
			for _, mode := range []string{"direct", "proxy"} {
				t.Run(mode, func(t *testing.T) {
					c := testConsole(t)
					dns := &coldCandidateDNS{fixtureDNS: testDNS(c), answers: []string{"192.0.2.1", "2001:db8::1", "192.0.2.2", "2001:db8::2", "192.0.2.3", "2001:db8::3"}}
					if cache == "warm" {
						dns.seed("catalog.invalid", dns.answers...)
					}
					c.dns = dns
					s := c.settings
					s.UpstreamMode, s.ProxyBackend = mode, "external-socks5"
					s.SOCKS5Address = "127.0.0.1:9" // Selection must not dial the configured proxy.
					_, client, err := c.networkFor(s)
					if err != nil {
						t.Fatal(err)
					}
					c.client.Transport.(interface{ Retire() }).Retire()
					c.client, c.settings = client, s
					clients := c.operationClients(context.Background(), upstreamstate.Catalog, upstreamstate.Constraints{Entry: "https://catalog.invalid/list"})
					if mode == "proxy" {
						if len(clients) != 1 || clients[0] != client || dns.queries.Load() != 0 {
							t.Fatal("proxy must use the active client without DNS or candidate selection")
						}
						return
					}
					var direct, proxy int
					seen := make(map[string]bool)
					for _, client := range clients {
						id := client.Transport.(interface{ ChannelID() string }).ChannelID()
						if seen[id] {
							t.Fatal("duplicate fallback", id)
						}
						seen[id] = true
						if strings.HasPrefix(id, "direct/") {
							direct++
						} else {
							proxy++
						}
					}
					wantDirect, wantProxy := 6, 0
					if mode == "proxy" {
						wantDirect, wantProxy = 0, 1
					}
					if direct != wantDirect || proxy != wantProxy || len(clients) > upstreamrequest.MaxOperationCandidates {
						t.Fatalf("mode=%s direct=%d proxy=%d", mode, direct, proxy)
					}
					if mode == "proxy" && dns.queries.Load() != 0 {
						t.Fatal("forced proxy queried target DNS")
					}
					if cache == "cold" && mode != "proxy" && dns.queries.Load() != 1 {
						t.Fatal("cold direct membership was not discovered")
					}
				})
			}
		})
	}
}

func TestColdCatalogCancellationStopsDiscovery(t *testing.T) {
	c := testConsole(t)
	channels := &coldBusinessChannels{RoundTripper: catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled discovery attempted an HTTP request")
		return nil, nil
	})}
	c.client.Transport, c.checksumURL = channels, "https://catalog.invalid/list"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.fetchCatalogSnapshot(ctx); !errors.Is(err, context.Canceled) || channels.ready {
		t.Fatalf("ready=%v err=%v", channels.ready, err)
	}
}

func (p *coldBusinessChannels) Candidates(ctx context.Context, _ string) ([]upstreamrequest.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.ready = true
	return p.channels, nil
}
func (p *coldBusinessChannels) Current(string) []upstreamrequest.Candidate {
	if !p.ready {
		return nil
	}
	return p.channels
}
func (*coldBusinessChannels) Changed() <-chan struct{} { return nil }

func TestColdCatalogFallsBackAfterTLSFailure(t *testing.T) {
	c := testConsole(t)
	var first, second atomic.Int32
	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		writeTestMD5Catalog(w, map[int]string{42: "video"})
	}))
	defer good.Close()
	// Accept TCP, then close during the TLS handshake, matching the real EOF.
	bad, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	go func() {
		for {
			conn, err := bad.Accept()
			if err != nil {
				return
			}
			first.Add(1)
			conn.Close()
		}
	}()
	failed := good.Client().Transport.(*http.Transport).Clone()
	failed.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, bad.Addr().String())
	}
	defer failed.CloseIdleConnections()
	c.checksumURL = good.URL
	c.client = good.Client()
	c.client.Transport = &coldBusinessChannels{RoundTripper: failed, channels: []upstreamrequest.Candidate{
		{ID: "direct/fixture/::1", Mode: "direct", Transport: failed},
		{ID: "direct/fixture/127.0.0.1", Mode: "direct", Transport: good.Client().Transport},
	}}
	catalog, err := c.fetchCatalogSnapshot(context.Background())
	// The first ranked path exhausts HTTPS and its qualified HTTP fallback.
	if err != nil || len(catalog.Songs) != 1 || first.Load() != 2 || second.Load() != 1 {
		t.Fatalf("songs=%d first=%d second=%d err=%v", len(catalog.Songs), first.Load(), second.Load(), err)
	}
}

func TestColdDirectBusinessDiscoversPath(t *testing.T) {
	for _, operation := range []string{"catalog", "playback"} {
		t.Run(operation, func(t *testing.T) {
			c := testConsole(t)
			var requests atomic.Int32
			const playback = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if operation == "catalog" {
					writeTestMD5Catalog(w, map[int]string{42: "video"})
				} else {
					http.Redirect(w, r, playback, http.StatusFound)
				}
			}))
			defer origin.Close()
			dns := &coldCandidateDNS{fixtureDNS: testDNS(c), answers: []string{"127.0.0.1"}}
			c.dns = dns
			s := c.settings
			s.UpstreamMode = "direct"
			_, client, err := c.networkFor(s)
			if err != nil {
				t.Fatal(err)
			}
			c.client.Transport.(interface{ Retire() }).Retire()
			c.client, c.settings = client, s
			c.apiBase = strings.Replace(origin.URL, "127.0.0.1", "catalog.invalid", 1)
			c.checksumURL = c.apiBase + "/catalog"
			current := client.Transport.(upstreamrequest.Candidates).Current(c.checksumURL)
			if len(current) != 0 || dns.queries.Load() != 0 || c.monitor != nil {
				t.Fatal("fixture is not cold direct")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if operation == "catalog" {
				catalog, err := c.fetchCatalogSnapshot(ctx)
				if err != nil || len(catalog.Songs) != 1 {
					t.Fatalf("songs=%d err=%v", len(catalog.Songs), err)
				}
			} else {
				target, err := c.resolveSongURL(ctx, 42)
				if err != nil || target != playback {
					t.Fatalf("target=%q err=%v", target, err)
				}
			}
			if requests.Load() != 1 || dns.queries.Load() != 1 {
				t.Fatalf("direct requests=%d DNS queries=%d", requests.Load(), dns.queries.Load())
			}
		})
	}
}
