package upstreamrequest

import (
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

func TestDirectPoolInjectedDialer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "example.com" || r.TLS.ServerName != "example.com" {
			t.Error("lost Host or SNI", r.Host, r.TLS.ServerName)
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	dns := &testDNS{addresses: []Address{{IP: "127.0.0.1", Expires: time.Now().Add(time.Minute)}}}
	target := "https://example.com:" + port + "/"
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned-host-tls-reuse", true: "failure-no-fallback"}[fail], func(t *testing.T) {
			calls := 0
			denied := errors.New("injected dial rejection")
			p, err := NewDirectPool(dns, func(ctx context.Context, network, address string) (net.Conn, error) {
				calls++
				if address != server.Listener.Addr().String() {
					t.Errorf("did not pin IP: %s", address)
				}
				if fail {
					return nil, denied
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			cs, err := p.Candidates(context.Background(), target)
			if err != nil || len(cs) != 1 {
				t.Fatal(cs, err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			for _, tr := range p.transports {
				tr.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			client := &http.Client{Transport: cs[0].Transport, Timeout: time.Second}
			for range 2 {
				resp, err := client.Get(target)
				if fail {
					if !errors.Is(err, denied) || resp != nil {
						t.Fatal("dial rejection bypassed", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || len(resp.TLS.VerifiedChains) == 0 {
					t.Fatal("TLS/request failed", err)
				}
			}
			want := 1
			if fail {
				want = 2
			}
			if calls != want {
				t.Fatal("unexpected dialing/reuse", calls, want)
			}
			p.Close()
			if _, err := client.Get(target); !errors.Is(err, ErrUnavailable) {
				t.Fatal("closed pool revived", err)
			}
		})
	}
	if _, err := NewDirectPool(dns, nil); err == nil {
		t.Fatal("nil dialer allowed")
	}
}
