package upstreamrequest

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type testDNS struct {
	addresses []Address
	calls     int
	err       error
	changed   chan struct{}
}

func (d *testDNS) Addresses(context.Context, string) ([]Address, error) {
	d.calls++
	return d.addresses, d.err
}
func (d *testDNS) CurrentAddresses(string) []Address { return d.addresses }
func (d *testDNS) DNSChanged() <-chan struct{}       { return d.changed }

func TestDirectDiscoveryPreservesCachedPathsAndReportsFailure(t *testing.T) {
	d := &testDNS{addresses: []Address{{"203.0.113.1", time.Now().Add(time.Minute)}}, err: errors.New("DNS unavailable")}
	p, err := NewPool("direct", d, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cs, err := p.Candidates(context.Background(), "https://example.com")
	if !errors.Is(err, d.err) || len(cs) != 1 || cs[0].IP != "203.0.113.1" {
		t.Fatal("failed discovery lost usable lease or claimed confirmed membership", cs, err)
	}
}

func TestPoolsRejectRetiredAutoMode(t *testing.T) {
	d := &testDNS{}
	if _, err := NewPool("auto", d, nil); err == nil {
		t.Fatal("single pool accepted auto")
	}
	if _, err := NewMultiPool("auto", d, nil); err == nil {
		t.Fatal("multi pool accepted auto")
	}
}

type refreshingTestDNS struct {
	testDNS
	refresh func(context.Context) ([]Address, error)
}

func (d *refreshingTestDNS) Addresses(ctx context.Context, _ string) ([]Address, error) {
	d.calls++
	addresses, err := d.refresh(ctx)
	if err == nil {
		d.addresses = addresses
	}
	return addresses, err
}

func TestPinnedCandidateRefreshesExpiredDNSWithoutChangingIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exercise a new connection for each block, as calibration ramps up.
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	target := "http://fixture.example:" + port + "/video"
	fresh := func(ip string) []Address { return []Address{{ip, time.Now().Add(time.Minute)}} }
	d := &refreshingTestDNS{refresh: func(context.Context) ([]Address, error) { return fresh("127.0.0.1"), nil }}
	p, err := NewPool("direct", d, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cs, err := p.Candidates(context.Background(), target)
	if err != nil || len(cs) != 1 {
		t.Fatalf("initial candidates: %v %v", cs, err)
	}
	client := &http.Client{Transport: cs[0].Transport, Timeout: time.Second}
	request := func() error {
		resp, err := client.Get(target)
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	if err := request(); err != nil || d.calls != 1 {
		t.Fatalf("valid candidate should reuse DNS: calls=%d err=%v", d.calls, err)
	}
	for run := 1; run <= 3; run++ {
		d.addresses[0].Expires = time.Now().Add(-time.Second)
		if err := request(); err != nil || d.calls != run+1 {
			t.Fatalf("request %d failed after TTL expiry: calls=%d err=%v", run, d.calls, err)
		}
	}
	// A fresh answer must confirm the same pinned IP; never rotate silently.
	d.addresses[0].Expires = time.Now().Add(-time.Second)
	d.refresh = func(context.Context) ([]Address, error) { return fresh("127.0.0.2"), nil }
	if err := request(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("removed pinned IP accepted: %v", err)
	}
	// Failed DNS must not resurrect the expired address.
	d.addresses = []Address{{"127.0.0.1", time.Now().Add(-time.Second)}}
	d.refresh = func(context.Context) ([]Address, error) { return nil, errors.New("DNS offline") }
	if err := request(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed refresh accepted: %v", err)
	}
	// Foreign hosts must not initiate a DNS lookup.
	calls := d.calls
	r, _ := http.NewRequest(http.MethodGet, "http://other.example/video", nil)
	if _, err := cs[0].Transport.RoundTrip(r); !errors.Is(err, ErrUnavailable) || d.calls != calls {
		t.Fatalf("foreign host refreshed: calls=%d err=%v", d.calls, err)
	}
	// Cancellation during refresh remains cancellation, not a route failure.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.refresh = func(ctx context.Context) ([]Address, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	r, _ = http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if _, err := cs[0].Transport.RoundTrip(r); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh cancellation lost: %v", err)
	}
	// Retirement during refresh must still reject a fresh answer.
	d.refresh = func(context.Context) ([]Address, error) {
		p.Close()
		return fresh("127.0.0.1"), nil
	}
	if err := request(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("retirement during refresh ignored: %v", err)
	}
	calls = d.calls
	if err := request(); !errors.Is(err, ErrUnavailable) || d.calls != calls {
		t.Fatalf("retired candidate refreshed: calls=%d err=%v", d.calls, err)
	}
}

func TestCandidateModesAndRemoteDNS(t *testing.T) {
	for _, mode := range []string{"direct", "socks5"} {
		t.Run(mode, func(t *testing.T) {
			d := &testDNS{addresses: []Address{{"203.0.113.1", time.Now().Add(time.Minute)}}}
			var destination string
			p, err := NewPool(mode, d, func(ctx context.Context, network, address string) (net.Conn, error) {
				destination = address
				a, b := net.Pipe()
				go func() {
					defer b.Close()
					_, _ = http.ReadRequest(bufio.NewReader(b))
					_, _ = io.WriteString(b, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
				}()
				return a, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			cs, err := p.Candidates(context.Background(), "http://example.com/test")
			want := 1
			if err != nil || len(cs) != want {
				t.Fatal(cs, err)
			}
			if mode == "socks5" && d.calls != 0 {
				t.Fatal("forced proxy queried local DNS")
			}
			for _, c := range cs {
				if c.Mode == "socks5" {
					r, _ := http.NewRequest("GET", "http://example.com/test", nil)
					resp, err := c.Transport.RoundTrip(r)
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
					if destination != "example.com:80" {
						t.Fatal("proxy did not receive domain", destination)
					}
				}
			}
		})
	}
}

func TestPinnedTLSHostExpiryAndConfigRetirement(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) { testPinnedTLSHostExpiryAndConfigRetirement(t, ip) })
	}
}

func testPinnedTLSHostExpiryAndConfigRetirement(t *testing.T, ip string) {
	var host, sni string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		sni = r.TLS.ServerName
		w.WriteHeader(204)
	}))
	server.Listener.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.StartTLS()
	defer server.Close()
	cert := server.Certificate()
	name := cert.DNSNames[0]
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	target := "https://" + net.JoinHostPort(name, port) + "/test"
	d := &testDNS{addresses: []Address{{ip, time.Now().Add(time.Minute)}}}
	p, _ := NewPool("direct", d, nil)
	defer p.Close()
	cs, _ := p.Candidates(context.Background(), target)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	p.transports[cs[0].ID].TLSClientConfig = &tls.Config{RootCAs: roots}
	r, _ := http.NewRequest("GET", target, nil)
	resp, err := cs[0].Transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if host != net.JoinHostPort(name, port) || sni != name {
		t.Fatal(host, sni)
	}
	d.addresses[0].Expires = time.Now().Add(time.Minute * 2)
	if p.Current(target)[0].ID != cs[0].ID {
		t.Fatal("TTL refresh changed identity")
	}
	d.addresses[0].Expires = time.Now().Add(-time.Second)
	if len(p.Current(target)) != 0 {
		t.Fatal("expired IP offered")
	}
	if _, err := cs[0].Transport.RoundTrip(r); !errors.Is(err, ErrUnavailable) {
		t.Fatal("expired candidate used", err)
	}
	d.addresses[0].Expires = time.Now().Add(time.Minute)
	p.Close()
	if _, err := cs[0].Transport.RoundTrip(r); !errors.Is(err, ErrUnavailable) {
		t.Fatal("retired config used", err)
	}
}
