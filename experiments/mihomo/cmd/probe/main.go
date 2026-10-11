// Client-only process: no inbound, config loader, or full tunnel initialization.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/proxydialer"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	C "github.com/metacubex/mihomo/constant"
	"github.com/miekg/dns"
	"still-wanna-dance/internal/upstreamrequest"
)

type fixedResolver struct {
	mu      sync.Mutex
	queries []string
	proxy   bool
}

func (r *fixedResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	r.queries = append(r.queries, host)
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.proxy && host == "cancel.invalid" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if r.proxy && host == "proxy.invalid" {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	return nil, errors.New("blocked unexpected DNS lookup")
}
func (r *fixedResolver) LookupIPv4(c context.Context, h string) ([]netip.Addr, error) {
	return r.LookupIP(c, h)
}
func (r *fixedResolver) LookupIPv6(c context.Context, h string) ([]netip.Addr, error) {
	return r.LookupIP(c, h)
}
func (*fixedResolver) ResolveECH(context.Context, string) ([]byte, error) {
	return nil, errors.New("ECH outside experiment")
}
func (*fixedResolver) ExchangeContext(context.Context, *dns.Msg) (*dns.Msg, error) {
	return nil, errors.New("DNS exchange outside experiment")
}
func (*fixedResolver) Invalid() bool    { return true }
func (*fixedResolver) ClearCache()      {}
func (*fixedResolver) ResetConnection() {}

type check struct {
	Name         string `json:"name"`
	Passed       bool   `json:"passed"`
	Detail       string `json:"detail,omitempty"`
	Milliseconds int64  `json:"milliseconds"`
}

var checks []check

func record(name string, fn func() (bool, string)) {
	start := time.Now()
	ok, why := fn()
	checks = append(checks, check{name, ok, why, time.Since(start).Milliseconds()})
}
func detail(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
func dial(p C.Proxy, ctx context.Context) (net.Conn, error) {
	return p.DialContext(ctx, &C.Metadata{NetWork: C.TCP, Type: C.INNER, Host: "business.invalid", DstPort: 80})
}
func request(p C.Proxy, path string, limit time.Duration) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	conn, err := dial(p, ctx)
	if err != nil {
		return err
	}
	// Close synchronously before the next request. net/http.Transport closes
	// a non-keepalive connection asynchronously after delivering the response,
	// which can legitimately require another AnyTLS session in a tight loop.
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// The context callback bounds I/O. A separate socket deadline at the same
	// instant can win the timer race and return a net.Error before ctx.Err().
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://business.invalid"+path, nil)
	if err = req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 || string(b) != "mihomo-embedded-business-response\n" {
		return fmt.Errorf("unexpected response status=%d bytes=%d", resp.StatusCode, len(b))
	}
	return nil
}
func run() error {
	manifest := flag.String("fixture", "", "fixture manifest")
	output := flag.String("output", "", "result JSON")
	hold := flag.Duration("hold", 0, "hold before and after checks for OS inspection")
	afterClose := flag.Bool("exercise-after-close", false, "observe raw post-Close behavior; may leave recreated sessions until process exit")
	flag.Parse()
	_ = upstreamrequest.NewChannel()
	initial := map[string]bool{"systemResolver": resolver.SystemResolver != nil, "proxyResolver": resolver.ProxyServerHostResolver != nil, "defaultResolver": resolver.DefaultResolver != nil, "useSystemHosts": resolver.UseSystemHosts}
	time.Sleep(*hold)
	data, err := os.ReadFile(*manifest)
	if err != nil {
		return err
	}
	var fixture struct {
		Nodes    []map[string]any `json:"nodes"`
		StatsURL string           `json:"statsURL"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		return err
	}
	proxyDNS := &fixedResolver{proxy: true}
	otherDNS := &fixedResolver{}
	resolver.ProxyServerHostResolver = proxyDNS
	resolver.DefaultResolver = otherDNS
	resolver.SystemResolver = otherDNS
	resolver.DefaultHosts = resolver.NewHosts(trie.New[resolver.HostValue]())
	resolver.UseSystemHosts = false
	resolver.DisableSystemHosts = true
	for _, node := range fixture.Nodes {
		if err := checkGuard(node); err != nil {
			return err
		}
		name := node["type"].(string)
		p, err := adapter.ParseProxy(node)
		if err != nil {
			checks = append(checks, check{Name: name + "/parse", Detail: err.Error()})
			continue
		}
		record(name+"/http-and-session-reuse", func() (bool, string) {
			before, err := transportCount(fixture.StatsURL, name)
			if err != nil {
				return false, err.Error()
			}
			for range 3 {
				if err := request(p, "/", 3*time.Second); err != nil {
					return false, err.Error()
				}
			}
			after, err := transportCount(fixture.StatsURL, name)
			if err != nil {
				return false, err.Error()
			}
			want := int64(3)
			if name == "anytls" || name == "hysteria2" {
				want = 1
			}
			return after-before == want, fmt.Sprintf("three HTTP connections; server transport count delta=%d expected=%d", after-before, want)
		})
		record(name+"/concurrent-http", func() (bool, string) {
			var wg sync.WaitGroup
			results := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() { defer wg.Done(); results <- request(p, "/", 3*time.Second) }()
			}
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					return false, err.Error()
				}
			}
			return true, ""
		})
		record(name+"/http-deadline", func() (bool, string) {
			err := request(p, "/stall", 200*time.Millisecond)
			return errors.Is(err, context.DeadlineExceeded), detail(err)
		})
		record(name+"/pre-canceled-dial", func() (bool, string) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			conn, err := dial(p, ctx)
			if conn != nil {
				conn.Close()
			}
			return errors.Is(err, context.Canceled), detail(err)
		})
		p.Close()
		if *afterClose {
			record(name+"/close-rejects-new-business", func() (bool, string) { err := request(p, "/", 500*time.Millisecond); return err != nil, detail(err) })
			p.Close()
		}
		bad := maps.Clone(node)
		if name == "vless" {
			bad["uuid"] = "ac94f096-1c92-4efb-a764-c1e1c391bb8e"
		} else {
			bad["password"] = "wrong-experiment-password"
		}
		wrong, err := adapter.ParseProxy(bad)
		if err != nil {
			return err
		}
		record(name+"/invalid-auth-fails-business", func() (bool, string) {
			err := request(wrong, "/", 500*time.Millisecond)
			return err != nil, detail(err)
		})
		wrong.Close()
		if name != "ss" {
			bad = maps.Clone(node)
			bad["fingerprint"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			wrong, err := adapter.ParseProxy(bad)
			if err != nil {
				return err
			}
			record(name+"/tls-pin-enforced", func() (bool, string) {
				err := request(wrong, "/", 500*time.Millisecond)
				return err != nil, detail(err)
			})
			wrong.Close()
		}
	}
	if len(fixture.Nodes) >= 2 {
		parent, err := adapter.ParseProxy(fixture.Nodes[0])
		if err != nil {
			return err
		}
		defer parent.Close()
		child := maps.Clone(fixture.Nodes[1])
		child["dialer-proxy"] = "missing-parent"
		p, err := adapter.ParseProxy(child)
		if err != nil {
			return err
		}
		record("chain/unbound-name-rejected", func() (bool, string) { err := request(p, "/", time.Second); return err != nil, detail(err) })
		p.Close()
		delete(child, "dialer-proxy")
		p, err = adapter.ParseProxy(child, adapter.WithDialerForAPI(proxydialer.New(parent, false)))
		if err != nil {
			return err
		}
		record("chain/fixed-parent-object-http", func() (bool, string) { err := request(p, "/", 3*time.Second); return err == nil, detail(err) })
		p.Close()
	}
	proxyDNS.mu.Lock()
	proxyQueries := append([]string(nil), proxyDNS.queries...)
	proxyDNS.mu.Unlock()
	otherDNS.mu.Lock()
	otherQueries := append([]string(nil), otherDNS.queries...)
	otherDNS.mu.Unlock()
	record("dns/proxy-host-resolver-used", func() (bool, string) { return len(proxyQueries) > 0, fmt.Sprint(len(proxyQueries)) })
	record("dns/no-local-business-or-system-resolution", func() (bool, string) { return len(otherQueries) == 0, fmt.Sprint(otherQueries) })
	data, err = json.MarshalIndent(map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "pid": os.Getpid(), "initialGlobals": initial, "checks": checks, "proxyDNSQueries": proxyQueries, "unexpectedDNSQueries": otherQueries}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(*output, data, 0600); err != nil {
		return err
	}
	time.Sleep(*hold)
	if f, err := os.Create(*output + ".goroutines.txt"); err == nil {
		_ = pprof.Lookup("goroutine").WriteTo(f, 2)
		f.Close()
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
