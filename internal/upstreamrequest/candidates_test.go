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
	"strings"
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

func TestCandidateModesAndRemoteDNS(t *testing.T) {
	for _, mode := range []string{"direct", "socks5", "auto"} {
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
			if mode == "auto" {
				want = 2
			}
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
	var host, sni string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		sni = r.TLS.ServerName
		w.WriteHeader(204)
	}))
	defer server.Close()
	cert := server.Certificate()
	name := cert.DNSNames[0]
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	target := "https://" + net.JoinHostPort(name, port) + "/test"
	d := &testDNS{addresses: []Address{{"127.0.0.1", time.Now().Add(time.Minute)}}}
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

func TestAutoKeepsProxyWhenDNSFails(t *testing.T) {
	d := &testDNS{err: errors.New("offline")}
	p, _ := NewPool("auto", d, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") })
	defer p.Close()
	cs, err := p.Candidates(context.Background(), "https://example.com")
	if err != nil || len(cs) != 1 || cs[0].Mode != "socks5" || strings.Contains(cs[0].ID, "@") {
		t.Fatal(cs, err)
	}
}
