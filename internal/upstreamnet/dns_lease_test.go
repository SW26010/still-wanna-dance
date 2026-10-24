package upstreamnet

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// A new short answer must not revoke a lease granted by an earlier answer.
// Omitted and repeated addresses have the same original-TTL guarantee.
func TestDNSRefreshRetainsOriginalLease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ttl    time.Duration
		omitV6 bool
	}{
		{"short_dual_stack", time.Second, false},
		{"zero_dual_stack", 0, false},
		{"short_omits_v6", time.Second, true},
		{"long_dual_stack", time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := &Resolver{}
				defer d.Close()
				const host, v4, v6 = "example.com", "192.0.2.1", "2001:db8::1"
				var calls atomic.Int32
				start := time.Now()
				_, err := d.lookupShared(context.Background(), host, func(context.Context) ([]string, time.Duration, error) {
					if calls.Add(1) == 1 {
						return []string{v4, v6}, time.Minute, nil
					}
					if tc.omitV6 {
						return []string{v4}, tc.ttl, nil
					}
					return []string{v4, v6}, tc.ttl, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(30 * time.Second)
				synctest.Wait()
				d.setBackgroundEnabled(false)
				if calls.Load() != 2 {
					t.Fatal("expected one automatic refresh")
				}
				d.mu.Lock()
				cacheUntil := d.cache[host].until
				d.mu.Unlock()
				if !cacheUntil.Equal(start.Add(30*time.Second + tc.ttl)) {
					t.Fatal("refresh cache must still use the new answer TTL")
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				current := d.CurrentAddresses(host)
				if len(current) != 2 {
					t.Fatalf("still-valid original dual-stack leases lost: %v", current)
				}
				for _, a := range current {
					want := start.Add(time.Minute)
					if !(tc.omitV6 && a.IP == v6) && cacheUntil.After(want) {
						want = cacheUntil
					}
					if !a.Expires.Equal(want) {
						t.Fatalf("lease %s expires at %v, want %v", a.IP, a.Expires, want)
					}
				}
				expiry := start.Add(max(time.Minute, 30*time.Second+tc.ttl))
				time.Sleep(time.Until(expiry))
				synctest.Wait()
				if got := d.CurrentAddresses(host); len(got) != 0 {
					t.Fatalf("leases survived all real answer TTLs: %v", got)
				}
			})
		})
	}
}
