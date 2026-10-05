package console

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
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

func relayListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func runTestRelay(t *testing.T, dial func(context.Context, string, string) (net.Conn, error)) *httpsRelay {
	t.Helper()
	p := newHTTPSRelay(relayListener(t), dial)
	done := make(chan error, 1)
	go func() { done <- p.serve() }()
	t.Cleanup(func() {
		p.close()
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return p
}

func relayCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
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

func TestHTTPSRelayPreservesTLSAndRange(t *testing.T) {
	cert, roots := relayCertificate(t)
	for _, host := range []string{"nya.xin.moe", "play.udon.dance", "api.udon.dance"} {
		for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
			t.Run(host+"/"+tls.VersionName(version), func(t *testing.T) {
				path := "/video.mp4"
				if host == "api.udon.dance" {
					path = "/Api/Songs/list"
				}
				backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.TLS.ServerName != host || r.Host != host || r.URL.Path != path || r.URL.RawQuery != "signed=unchanged" {
						t.Errorf("altered request: host=%s SNI=%s URL=%s", r.Host, r.TLS.ServerName, r.URL)
					}
					http.ServeContent(w, r, "video.mp4", time.Time{}, strings.NewReader("0123456789"))
				}))
				backend.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
				backend.EnableHTTP2 = true
				backend.StartTLS()
				defer backend.Close()
				p := runTestRelay(t, func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != net.JoinHostPort(host, "443") || network != "tcp4" {
						t.Errorf("wrong upstream: %s %s", network, address)
					}
					return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
				})
				transport := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: version, MaxVersion: version}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, p.listener.Addr().String())
				}}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
				for _, method := range []string{"GET", "HEAD"} {
					req, _ := http.NewRequest(method, "https://"+host+path+"?signed=unchanged", nil)
					req.Header.Set("Range", "bytes=3-6")
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil || resp.StatusCode != 206 || resp.Header.Get("Content-Range") != "bytes 3-6/10" || resp.ProtoMajor != 2 || len(resp.TLS.VerifiedChains) == 0 {
						t.Fatalf("response: %s %v %v", resp.Status, resp.Header, err)
					}
					if (method == "GET" && string(body) != "3456") || (method == "HEAD" && len(body) != 0) {
						t.Fatalf("body: %q", body)
					}
				}
			})
		}
	}
}

func TestHTTPSRelayRejectsUnmanagedAndMalformedClients(t *testing.T) {
	var calls atomic.Int32
	p := runTestRelay(t, func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("must not dial")
	})
	for _, name := range []string{"example.com", "127.0.0.1", "nya.xin.moe.evil.test"} {
		client, err := net.Dial("tcp4", p.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		client.SetDeadline(time.Now().Add(time.Second))
		if err = tls.Client(client, &tls.Config{ServerName: name}).Handshake(); err == nil {
			t.Fatal("unmanaged SNI accepted")
		}
		client.Close()
	}
	client, err := net.Dial("tcp4", p.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(client, "GET / HTTP/1.1\r\nHost: nya.xin.moe\r\n\r\n")
	if _, err = client.Read(make([]byte, 1)); err == nil {
		t.Fatal("plaintext client accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("unmanaged client caused an upstream connection")
	}
}

// Fragment a real ClientHello across TLS records and verify byte-for-byte replay.
func TestClientHelloFragmentation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(3 * time.Second))
	b.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		tls.Client(a, &tls.Config{ServerName: "nya.xin.moe"}).Handshake()
	}()
	var header [5]byte
	if _, err := io.ReadFull(b, header[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(header[3:]))
	if _, err := io.ReadFull(b, body); err != nil {
		t.Fatal(err)
	}
	b.Close()
	<-done
	var fragmented []byte
	for len(body) > 0 {
		n := min(37, len(body))
		binary.BigEndian.PutUint16(header[3:], uint16(n))
		fragmented = append(fragmented, header[:]...)
		fragmented = append(fragmented, body[:n]...)
		body = body[n:]
	}
	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()
	writer.SetDeadline(time.Now().Add(3 * time.Second))
	go func() { writer.Write(fragmented) }()
	name, captured, err := readClientHello(reader)
	if err != nil || name != "nya.xin.moe" || string(captured) != string(fragmented) {
		t.Fatalf("fragmented hello: %s bytes=%d/%d error=%v", name, len(captured), len(fragmented), err)
	}
}

func TestHTTPSRelayCloseCancelsDialAndIncompleteHello(t *testing.T) {
	dialing := make(chan struct{})
	p := runTestRelay(t, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialing)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	incomplete, err := net.Dial("tcp4", p.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer incomplete.Close()
	client, err := net.Dial("tcp4", p.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	go tls.Client(client, &tls.Config{ServerName: "nya.xin.moe"}).Handshake()
	select {
	case <-dialing:
	case <-time.After(3 * time.Second):
		t.Fatal("no upstream dial")
	}
	done := make(chan struct{})
	go func() { p.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay shutdown hung")
	}
}

func TestHTTPSPortConflictRollsBackAndRestarts(t *testing.T) {
	c := testConsole(t)
	httpPort := relayListener(t)
	c.videoAddress = httpPort.Addr().String()
	httpPort.Close()
	occupied := relayListener(t)
	defer occupied.Close()
	c.httpsAddress = occupied.Addr().String()
	if err := c.start(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("missing HTTPS port error: %v", err)
	}
	if c.httpServer != nil || c.https != nil || c.service != nil || portAvailable(c.videoAddress) != nil {
		t.Fatal("failed start left a partially running CDN")
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var status struct{ Running, PortOK, HTTPSPortOK bool }
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Running || !status.PortOK || status.HTTPSPortOK {
		t.Fatalf("incorrect port status: %s", w.Body.String())
	}
	occupied.Close()
	for i := 0; i < 2; i++ {
		if err := c.start(); err != nil {
			t.Fatal(err)
		}
		if c.https == nil || c.httpServer == nil {
			t.Fatal("missing listener")
		}
		if err := c.stop(); err != nil {
			t.Fatal(err)
		}
		if portAvailable(c.videoAddress) != nil || portAvailable(c.httpsAddress) != nil {
			t.Fatal("stop did not release both ports")
		}
	}
}

func TestHTTPSRelayCloseStopsEstablishedTunnel(t *testing.T) {
	remote, upstream := net.Pipe()
	defer remote.Close()
	defer upstream.Close()
	p := runTestRelay(t, func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
	client, err := net.Dial("tcp4", p.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	go tls.Client(client, &tls.Config{ServerName: "nya.xin.moe"}).Handshake()
	remote.SetDeadline(time.Now().Add(3 * time.Second))
	var header [5]byte
	if _, err = io.ReadFull(remote, header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = io.CopyN(io.Discard, remote, int64(binary.BigEndian.Uint16(header[3:]))); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { p.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("established tunnel blocked shutdown")
	}
	if _, err = remote.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("upstream was not closed: %v", err)
	}
}

func TestHTTPSStartupStorageFailureReleasesPorts(t *testing.T) {
	c := testConsole(t)
	video, secure := relayListener(t), relayListener(t)
	c.videoAddress, c.httpsAddress = video.Addr().String(), secure.Addr().String()
	video.Close()
	secure.Close()
	// The config's parent exists, but a NUL byte is not a valid directory.
	c.settings.StorageDir += "\x00"
	if err := c.start(); err == nil {
		t.Fatal("invalid storage accepted")
	}
	if portAvailable(c.videoAddress) != nil || portAvailable(c.httpsAddress) != nil {
		t.Fatal("engine failure leaked listeners")
	}
}
