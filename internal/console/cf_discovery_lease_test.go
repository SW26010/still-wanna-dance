//go:build windows && cf_block_experiment

package console

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Diagnostic reproduction of the current behavior, not an assertion that this
// is the desired lease policy. No production behavior is changed here.
func TestCFDiscoveryLeaseShortening(t *testing.T) {
	for _, omitV6 := range []bool{false, true} {
		name := "same_dual_stack_answer"
		if omitV6 {
			name = "v6_omitted_from_refresh"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := &directDNS{}
				defer d.close()
				const v4, v6 = "104.26.4.28", "2606:4700:20::681a:41c"
				var calls atomic.Int32
				_, err := d.lookupShared(context.Background(), cfexpHost, func(context.Context) ([]string, time.Duration, error) {
					if calls.Add(1) == 1 {
						return []string{v4, v6}, time.Minute, nil
					}
					if omitV6 {
						return []string{v4}, time.Second, nil
					}
					return []string{v4, v6}, time.Second, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				original := cfDiscoveryState(d, cfexpHost)
				time.Sleep(30 * time.Second)
				synctest.Wait()
				refreshed := cfDiscoveryState(d, cfexpHost)
				if calls.Load() != 2 || !refreshed.Leases[v4].Before(original.Leases[v4]) {
					t.Fatal("shorter refresh did not shorten returned address lease")
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				current := d.CurrentAddresses(cfexpHost)
				if !time.Now().Before(original.Leases[v4]) {
					t.Fatal("old answer must still be within its TTL")
				}
				if omitV6 {
					if len(current) != 1 || current[0].IP != v6 || !current[0].Expires.Equal(original.Leases[v6]) {
						t.Fatal("omitted address should retain previous unexpired lease")
					}
				} else if len(current) != 0 {
					t.Fatal("same returned IPs should reproduce the current empty pool")
				}
				t.Logf("old_answer_remaining=%.0fs returned_ip_lease_remaining=%.0fs candidates=%d", time.Until(original.Leases[v4]).Seconds(), time.Until(refreshed.Leases[v4]).Seconds(), len(current))
			})
		})
	}
}
