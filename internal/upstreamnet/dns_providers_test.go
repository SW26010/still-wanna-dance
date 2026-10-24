package upstreamnet

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoHProviderEmptyAnswerPreservesBootstrap(t *testing.T) {
	for _, mode := range []string{"empty", "alias-only", "HTTP failure", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "HTTP failure" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				packet, err := io.ReadAll(r.Body)
				if err != nil || len(packet) < 12 {
					t.Error("invalid query", err)
					return
				}
				packet[2], packet[3] = 0x81, 0x80
				if mode == "alias-only" {
					packet[7] = 1
					packet = append(packet, 0xc0, 12, 0, 5, 0, 1, 0, 0, 0, 60, 0, 7, 4, 'i', 'p', 'v', '4', 0xc0, 12)
				}
				if mode == "malformed" {
					packet[0] ^= 0xff
				}
				w.Header().Set("Content-Type", "application/dns-message")
				w.Write(packet)
			}))
			defer server.Close()
			entry := dnsEntry{[]string{"192.0.2.1", "192.0.2.2"}, time.Now().Add(time.Minute)}
			key := "bootstrap/provider.test"
			d := &Resolver{cache: map[string]dnsEntry{key: entry}}
			defer d.Close()
			for _, kind := range []uint16{dnsTypeAAAA, dnsTypeA} {
				calls.Store(0)
				d.cache[key] = entry
				ips, ttl, err := d.queryProviderIPs(context.Background(), "provider.test", entry.ips, func(ctx context.Context, _ string) ([]string, time.Duration, error) {
					return exchangeDoH(ctx, server.Client(), server.URL, "target.test", kind)
				})
				cached, present := d.cache[key]
				if mode == "empty" || mode == "alias-only" {
					if err != nil || len(ips) != 0 || ttl != 0 || calls.Load() != 1 || !present || !slices.Equal(cached.ips, entry.ips) || !cached.until.Equal(entry.until) {
						t.Fatalf("empty family %d: ips=%v ttl=%v error=%v calls=%d bootstrap=%+v", kind, ips, ttl, err, calls.Load(), cached)
					}
				} else if err == nil || calls.Load() != 2 || present {
					t.Fatalf("failed endpoint: error=%v calls=%d retained=%v", err, calls.Load(), present)
				}
			}
		})
	}
}
