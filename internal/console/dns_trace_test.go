package console

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"
)

// A real nested DoH exchange must not report its connection, TLS handshake or
// first response byte to the HTTP request whose dial caused the shared lookup.
func TestDNSLookupDoesNotInheritRequestTrace(t *testing.T) {
	doh := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		packet, err := io.ReadAll(r.Body)
		if err != nil || len(packet) < 12 {
			t.Error("invalid DNS query", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		packet[2], packet[3], packet[7] = 0x81, 0x80, 1
		packet = append(packet, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 203, 0, 113, 1)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packet)
	}))
	defer doh.Close()
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "target response")
	}))
	defer target.Close()
	d := &directDNS{}
	defer d.close()
	client := target.Client()
	transport := client.Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		_, err := d.lookupShared(ctx, "target.test", func(ctx context.Context) ([]string, time.Duration, error) {
			return exchangeDoH(ctx, doh.Client(), doh.URL, "target.test", dnsTypeA)
		})
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	client.Transport = transport
	var connections, handshakes, firstBytes atomic.Int32
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			connections.Add(1)
			if got := info.Conn.RemoteAddr().String(); got != target.Listener.Addr().String() {
				t.Errorf("request trace received nested DNS connection %s", got)
			}
		},
		TLSHandshakeDone:     func(tls.ConnectionState, error) { handshakes.Add(1) },
		GotFirstResponseByte: func() { firstBytes.Add(1) },
	}
	ctx, cancel := context.WithTimeout(httptrace.WithClientTrace(context.Background(), trace), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "target response" {
		t.Fatalf("target request: body=%q error=%v", body, err)
	}
	if connections.Load() != 1 || handshakes.Load() != 1 || firstBytes.Load() != 1 {
		t.Fatalf("request trace: connections=%d handshakes=%d firstBytes=%d; want one of each", connections.Load(), handshakes.Load(), firstBytes.Load())
	}
}
