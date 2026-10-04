package console

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDNSRefreshIsAutomaticAndKeepsValidAnswers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		defer d.close()
		if err := d.setPolicy(DNSPolicy{RefreshAhead: 20 * time.Second, MinInterval: 10 * time.Second, IdleTimeout: time.Minute, QueryTimeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		query := func(context.Context) ([]string, time.Duration, error) {
			n := calls.Add(1)
			if n == 2 || n == 3 {
				return nil, 0, errors.New("offline")
			}
			return []string{"203.0.113.1", "203.0.113.2"}, 25 * time.Second, nil
		}
		ips, err := d.lookupShared(context.Background(), "example.com", query)
		if err != nil {
			t.Fatal(err)
		}
		ips[0] = "mutated"
		expires := d.cache["example.com"].until
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 || d.states["example.com"].err == nil || d.cache["example.com"].until != expires {
			t.Fatal("refresh failure lost original TTL")
		}
		ips, err = d.lookupShared(context.Background(), "example.com", query)
		if err != nil || ips[0] != "203.0.113.1" || calls.Load() != 2 {
			t.Fatal("valid cache not served independently of failed refresh")
		}
		time.Sleep(16 * time.Second)
		synctest.Wait()
		if time.Now().Before(d.cache["example.com"].until) {
			t.Fatal("TTL extended by failure")
		}
		// Expired lookup cannot use stale data, even before the next background retry.
		_, err = d.lookupShared(context.Background(), "example.com", query)
		if err != nil || calls.Load() != 4 {
			t.Fatal("expired lookup did not refresh", err)
		}
	})
}

func TestDNSRefreshSharesForegroundAndPreservesWinner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		defer d.close()
		var calls atomic.Int32
		release := make(chan struct{})
		query := func(ctx context.Context) ([]string, time.Duration, error) {
			if calls.Add(1) > 1 {
				select {
				case <-release:
				case <-ctx.Done():
					return nil, 0, ctx.Err()
				}
			}
			return []string{"203.0.113.1", "203.0.113.2"}, 31 * time.Second, nil
		}
		_, _ = d.lookupShared(context.Background(), "example.com", query)
		d.mu.Lock()
		e := d.cache["example.com"]
		e.ips = []string{"203.0.113.2", "203.0.113.1"}
		d.cache["example.com"] = e
		d.mu.Unlock()
		time.Sleep(32 * time.Second)
		synctest.Wait()
		for range 10 {
			go func() {
				ips, err := d.lookupShared(context.Background(), "example.com", query)
				if err != nil || !slices.Equal(ips, []string{"203.0.113.2", "203.0.113.1"}) {
					t.Errorf("shared refresh: %v %v", ips, err)
				}
			}()
		}
		synctest.Wait()
		close(release)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("foreground duplicated background query")
		}
	})
}

func TestDNSRefreshPolicyIdleAndProxyPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		defer d.close()
		var calls atomic.Int32
		query := func(context.Context) ([]string, time.Duration, error) {
			calls.Add(1)
			return []string{"203.0.113.1"}, time.Minute, nil
		}
		_, _ = d.lookupShared(context.Background(), "example.com", query)
		d.setBackgroundEnabled(false)
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("background DNS queried while using proxy")
		}
		p := DefaultDNSPolicy()
		p.MinInterval = time.Second
		p.IdleTimeout = 2 * time.Minute
		if err := d.setPolicy(p); err != nil {
			t.Fatal(err)
		}
		d.setBackgroundEnabled(true)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("policy/resume did not refresh")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if len(d.states) != 0 || len(d.cache) != 0 {
			t.Fatal("idle state not reclaimed")
		}
		before := calls.Load()
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != before {
			t.Fatal("idle domain still queried")
		}
	})
}

func TestDNSCloseCancelsSharedQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		go func() {
			_, err := d.lookupShared(context.Background(), "example.com", func(ctx context.Context) ([]string, time.Duration, error) { <-ctx.Done(); return nil, 0, ctx.Err() })
			if !errors.Is(err, context.Canceled) {
				t.Errorf("close error: %v", err)
			}
		}()
		synctest.Wait()
		d.close()
		d.close()
		synctest.Wait()
		if len(d.lookups) != 0 {
			t.Fatal("query outlived close")
		}
		if _, err := d.lookup(context.Background(), "example.com"); err == nil {
			t.Fatal("lookup after close")
		}
	})
}
