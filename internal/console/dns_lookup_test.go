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

func TestDNSLookupSharesResults(t *testing.T) {
	offline := errors.New("offline")
	for _, tc := range []struct {
		name string
		ttl  time.Duration
		err  error
	}{
		{"cached", time.Minute, nil},
		{"zero TTL", 0, nil},
		{"failure", 0, offline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := &directDNS{cache: map[string]dnsEntry{
					"example.com": {[]string{"203.0.113.1"}, time.Now().Add(-time.Second)},
				}}
				var calls atomic.Int32
				want := []string{"203.0.113.9"}
				if tc.err != nil {
					want = nil
				}
				for round := range 2 {
					release := make(chan struct{})
					query := func(context.Context) ([]string, time.Duration, error) {
						calls.Add(1)
						<-release
						return want, tc.ttl, tc.err
					}
					for range 20 {
						go func() {
							ips, err := d.lookupShared(context.Background(), "example.com", query)
							if !slices.Equal(ips, want) || !errors.Is(err, tc.err) {
								t.Errorf("lookup = %v, %v; want %v, %v", ips, err, want, tc.err)
							}
						}()
					}
					synctest.Wait()
					close(release)
					synctest.Wait()
					wantCalls := int32(round + 1)
					if tc.ttl > 0 {
						wantCalls = 1
					}
					if calls.Load() != wantCalls {
						t.Fatalf("queries = %d, want %d", calls.Load(), wantCalls)
					}
					if len(d.lookups) != 0 {
						t.Fatal("completed lookup retained")
					}
				}
			})
		})
	}
}

func TestDNSLookupCancellationIsIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		release := make(chan struct{})
		var calls atomic.Int32
		query := func(ctx context.Context) ([]string, time.Duration, error) {
			calls.Add(1)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-release:
				return []string{"203.0.113.9"}, time.Minute, nil
			}
		}
		first, cancelFirst := context.WithCancel(context.Background())
		defer cancelFirst()
		waiter, cancelWaiter := context.WithCancel(context.Background())
		defer cancelWaiter()
		for _, ctx := range []context.Context{first, waiter, context.Background()} {
			go func() {
				ips, err := d.lookupShared(ctx, "example.com", query)
				if ctx.Err() != nil {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("canceled caller: %v", err)
					}
				} else if err != nil || !slices.Equal(ips, []string{"203.0.113.9"}) {
					t.Errorf("remaining caller: %v, %v", ips, err)
				}
			}()
			// Ensure the first caller owns the shared query.
			synctest.Wait()
		}
		cancelFirst()
		cancelWaiter()
		synctest.Wait()
		// A different domain must proceed while example.com is still blocked.
		_, err := d.lookupShared(context.Background(), "other.example", func(context.Context) ([]string, time.Duration, error) {
			return []string{"203.0.113.2"}, time.Minute, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		close(release)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("queries = %d, want 1", calls.Load())
		}
	})
}

func TestDNSLookupSharedQueryHasDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		start := time.Now()
		_, err := d.lookupShared(context.Background(), "example.com", func(ctx context.Context) ([]string, time.Duration, error) {
			<-ctx.Done()
			return nil, 0, ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 6*time.Second {
			t.Fatalf("shared deadline: elapsed %v, error %v", time.Since(start), err)
		}
		if len(d.lookups) != 0 || len(d.cache) != 0 {
			t.Fatal("timed-out lookup retained")
		}
	})
}
