package cacheproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func upstreamCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"nya.xin.moe", "play.udon.dance", "api.udon.dance"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func TestHTTPSOriginUsesIndependentDialAndVerifiedPublicName(t *testing.T) {
	cert, roots := upstreamCertificate(t)
	var requests atomic.Int32
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.TLS.ServerName != r.Host {
			t.Errorf("identity: host=%s sni=%s", r.Host, r.TLS.ServerName)
		}
		if r.Host == "nya.xin.moe" {
			w.Header().Set("Location", videoURL(payload))
			w.WriteHeader(302)
			return
		}
		io.WriteString(w, payload)
	}))
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	backend.StartTLS()
	defer backend.Close()
	for _, tc := range []struct {
		name     string
		trusted  bool
		override string
	}{
		{name: "public", trusted: true},
		{name: "explicit override", trusted: true, override: "origin.example:8443"},
		{name: "untrusted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig()
			cfg.StorageDir = t.TempDir()
			if tc.override != "" {
				for host := range cfg.Origins {
					cfg.Origins[host] = tc.override
				}
			}
			var dials atomic.Int32
			cfg.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				if tc.override != "" && address != tc.override {
					t.Errorf("override not used: %s", address)
				} else if tc.override == "" && address != "play.udon.dance:443" && address != "nya.xin.moe:443" {
					t.Errorf("unexpected origin %s", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			pool := x509.NewCertPool()
			if tc.trusted {
				pool = roots
			}
			s.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
			before := requests.Load()
			target := videoURL(payload)
			expectedDials := int32(1)
			if tc.trusted {
				target = strings.Replace(target, "play.udon.dance", "nya.xin.moe", 1)
				expectedDials = 2
			}
			w := request(s, "GET", target, nil)
			if tc.trusted {
				assertResponse(t, w, 200, payload)
			} else if w.Code != 502 || requests.Load() != before {
				t.Fatalf("untrusted TLS accepted: %d", w.Code)
			}
			if dials.Load() != expectedDials {
				t.Fatalf("fallback or missing built-in dial: %d", dials.Load())
			}
		})
	}
}

func TestUpstreamDialFailureNeverFallsBack(t *testing.T) {
	cfg := fixtureConfig()
	cfg.StorageDir = t.TempDir()
	sentinel := errors.New("built-in DNS unavailable")
	var calls atomic.Int32
	cfg.DialContext = func(context.Context, string, string) (net.Conn, error) { calls.Add(1); return nil, sentinel }
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Prefetch(context.Background(), videoURL(payload)); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retries", calls.Load())
	}
	transport := newTransport()
	if _, err := transport.DialContext(context.Background(), "tcp", "localhost:80"); err == nil {
		t.Fatal("system DNS was allowed")
	}
}

func TestPlaybackAPIStreamsAndReusesVideoCache(t *testing.T) {
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); io.WriteString(w, payload) })
	s.cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
		if id != "42" || node != "cf" {
			t.Errorf("id=%s node=%s", id, node)
		}
		return strings.Replace(videoURL(payload), "http:", "https:", 1), nil
	}
	target := "http://api.udon.dance/Api/Songs/play?id=42&node=cf"
	assertResponse(t, request(s, "GET", target, nil), 200, payload)
	assertResponse(t, request(s, "GET", target, map[string]string{"Range": "bytes=2-5"}), 206, payload[2:6])
	assertResponse(t, request(s, "HEAD", target, nil), 200, "")
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	if downloads.Load() != 1 {
		t.Fatalf("downloaded %d times", downloads.Load())
	}
	for _, query := range []string{"id=0", "id=42&id=43", "id=42&node=evil", "id=42&node=cf&node=nya", "id=42;bad=1"} {
		if w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?"+query, nil); w.Code != 400 {
			t.Errorf("%s: %d", query, w.Code)
		}
	}
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return "https://evil.invalid/video", nil }
	assertResponse(t, request(s, "GET", target, nil), 200, payload)
	if w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=43", nil); w.Code != 502 {
		t.Fatal("unknown song accepted invalid redirect", w.Code)
	}
	if downloads.Load() != 1 {
		t.Fatal("invalid redirect triggered download", downloads.Load())
	}
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/list", nil)
	if w.Code != 307 || w.Header().Get("Location") != "https://api.udon.dance/Api/Songs/list" {
		t.Fatal(w.Code, w.Header())
	}
}

func TestVideoRedirectRejectsChangedContentAndForeignHosts(t *testing.T) {
	for _, target := range []string{strings.Replace(videoURL(payload), "play.udon.dance", "evil.invalid", 1), videoURL("changed content")} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target)
				w.WriteHeader(302)
			})
			if w := request(s, "GET", videoURL(payload), nil); w.Code != 502 {
				t.Fatal(w.Code)
			}
			if calls.Load() != 1 {
				t.Fatal("followed invalid redirect", calls.Load())
			}
		})
	}
}
