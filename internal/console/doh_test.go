package console

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestDoHValidatesTLSAndWireResponse(t *testing.T) {
	for _, mode := range []string{"valid", "untrusted", "wrong-host", "wrong-id", "wrong-type", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/dns-message" {
					t.Error("not RFC8484")
				}
				q, _ := io.ReadAll(r.Body)
				if len(q) < 12 {
					t.Error("missing DNS query")
					return
				}
				q[2], q[3], q[7] = 0x81, 0x80, 1
				q = append(q, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 203, 0, 113, 9)
				w.Header().Set("Content-Type", "application/dns-message")
				if mode == "wrong-id" {
					q[0] ^= 0xff
				}
				if mode == "wrong-type" {
					w.Header().Set("Content-Type", "text/html")
				}
				if mode == "redirect" {
					w.Header().Set("Location", "http://plaintext.invalid")
					w.WriteHeader(302)
					return
				}
				w.Write(q)
			}))
			defer server.Close()
			transport := newDoHTransport(server.Listener.Addr().String())
			defer transport.CloseIdleConnections()
			if mode != "untrusted" {
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				transport.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			host := "example.com" // httptest's certificate covers example.com.
			if mode == "wrong-host" {
				host = "wrong.invalid"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ips, ttl, err := exchangeDoH(ctx, client, "https://"+host+"/dns-query", "api.udon.dance")
			if mode == "valid" {
				if err != nil || len(ips) != 1 || ips[0] != "203.0.113.9" || ttl != time.Minute {
					t.Fatal(ips, ttl, err)
				}
			} else if err == nil {
				t.Fatal("accepted unauthenticated/invalid reply", ips)
			}
		})
	}
}

func TestLiveDoHProviders(t *testing.T) {
	if os.Getenv("STEPSTASH_LIVE_DOH") != "1" {
		t.Skip("opt-in encrypted DNS connectivity check")
	}
	d := &directDNS{}
	for _, provider := range dohProviders {
		t.Run(provider.host+"/"+provider.ip, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			start := time.Now()
			ips, ttl, err := d.queryProvider(ctx, provider, "api.udon.dance")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("addresses=%v ttl=%v elapsed=%v", ips, ttl, time.Since(start))
		})
	}
}
