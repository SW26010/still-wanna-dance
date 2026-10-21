//go:build windows && cf_block_experiment

package console

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type cfDNSAnswer struct {
	LookupID           int
	Host               string
	Started            time.Time
	Provider, Endpoint string
	Kind               uint16
	IPs                []string
	TTLMS, ElapsedMS   float64
	Error              string
}
type cfDNSLookup struct {
	ID               int
	Host             string
	Started          time.Time
	IPs              []string
	TTLMS, ElapsedMS float64
	Error            string
}
type cfDNSObservation struct {
	Started    time.Time
	Addresses  []upstreamrequest.Address
	CacheIPs   []string
	CacheUntil time.Time
	Error      string
}
type cfDNSRecorder struct {
	d       *directDNS
	mu      sync.Mutex
	answers []cfDNSAnswer
	lookups []cfDNSLookup
	nextID  int
}

func (r *cfDNSRecorder) resolve(ctx context.Context, host string) ([]string, time.Duration, error) {
	started := time.Now()
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.mu.Unlock()
	var queries []dnsQuery
	for _, provider := range dohProviders {
		for _, kind := range []uint16{dnsTypeAAAA, dnsTypeA} {
			queries = append(queries, func(ctx context.Context) ([]string, time.Duration, error) {
				start := time.Now()
				ips, ttl, err := r.d.queryProvider(ctx, provider, host, kind)
				row := cfDNSAnswer{LookupID: id, Host: host, Started: start, Provider: provider.host, Endpoint: provider.ip, Kind: kind, IPs: ips, TTLMS: cfexpMS(ttl), ElapsedMS: cfexpMS(time.Since(start))}
				if err != nil {
					row.Error = "query_failed"
					if ctx.Err() != nil {
						row.Error = ctx.Err().Error()
					}
				}
				r.mu.Lock()
				r.answers = append(r.answers, row)
				r.mu.Unlock()
				return ips, ttl, err
			})
		}
	}
	ips, ttl, err := collectDNS(ctx, queries)
	row := cfDNSLookup{ID: id, Host: host, Started: started, IPs: ips, TTLMS: cfexpMS(ttl), ElapsedMS: cfexpMS(time.Since(started))}
	if err != nil {
		row.Error = "lookup_failed"
	}
	r.mu.Lock()
	r.lookups = append(r.lookups, row)
	r.mu.Unlock()
	return ips, ttl, err
}
func (r *cfDNSRecorder) Addresses(ctx context.Context, host string) ([]upstreamrequest.Address, error) {
	if host != cfexpHost && host != "api.udon.dance" {
		return nil, fmt.Errorf("unexpected host")
	}
	_, err := r.d.lookupShared(ctx, host, func(ctx context.Context) ([]string, time.Duration, error) { return r.resolve(ctx, host) })
	return r.d.CurrentAddresses(host), err
}
func (r *cfDNSRecorder) CurrentAddresses(host string) []upstreamrequest.Address {
	return r.d.CurrentAddresses(host)
}
func (r *cfDNSRecorder) DNSChanged() <-chan struct{} { return r.d.DNSChanged() }

// Observe the unchanged collector, lease store and candidate pool. Only record
// provider completion timings; no media downloads, DNS policy changes or retry.
func TestCFFamilyDNSExperiment(t *testing.T) {
	if *cfexpOut == "" {
		t.Skip("explicit -cfexp-out required")
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal("output must be new:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	r := &cfDNSRecorder{d: &directDNS{dialer: network}}
	defer r.d.close()
	pool, err := network.pool(r)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var observations []cfDNSObservation
	save := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := cfexpSave(filepath.Join(dir, "dns.json"), struct {
			Observations []cfDNSObservation
			Lookups      []cfDNSLookup
			Answers      []cfDNSAnswer
		}{observations, r.lookups, r.answers}); err != nil {
			t.Fatal(err)
		}
	}
	defer save()
	for i := 0; i < 120; i++ {
		row := cfDNSObservation{Started: time.Now()}
		cs, err := pool.Candidates(ctx, "https://"+cfexpHost)
		if err != nil {
			row.Error = "discovery_failed"
		}
		for _, c := range cs {
			row.Addresses = append(row.Addresses, upstreamrequest.Address{IP: c.IP, Expires: c.ValidUntil})
		}
		r.d.mu.Lock()
		row.CacheIPs = slices.Clone(r.d.cache[cfexpHost].ips)
		row.CacheUntil = r.d.cache[cfexpHost].until
		r.d.mu.Unlock()
		observations = append(observations, row)
		save()
		if i%15 == 0 || len(cs) != 6 {
			t.Logf("observation=%d candidates=%d cached=%d ttl=%.1fs error=%s", i+1, len(cs), len(row.CacheIPs), time.Until(row.CacheUntil).Seconds(), row.Error)
		}
		if err := cfexpPause(ctx, 2*time.Second); err != nil {
			t.Fatal("observation_context")
		}
	}
	// Wait for already canceled provider calls to record their outcome.
	r.d.close()
	if err := cfexpPause(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestCFFamilyDNSHarness(t *testing.T) {
	for _, fast := range []string{"104.26.4.28", "2606:4700:20::681a:41c"} {
		t.Run(fast, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				slow := "104.26.4.28"
				if fast == slow {
					slow = "2606:4700:20::681a:41c"
				}
				query := func(ip string, delay time.Duration) dnsQuery {
					return func(ctx context.Context) ([]string, time.Duration, error) {
						select {
						case <-time.After(delay):
							return []string{ip}, time.Minute, nil
						case <-ctx.Done():
							return nil, 0, ctx.Err()
						}
					}
				}
				ips, _, err := collectDNS(context.Background(), []dnsQuery{query(fast, 10*time.Millisecond), query(slow, 300*time.Millisecond)})
				if err != nil || !slices.Equal(ips, []string{fast}) {
					t.Fatalf("late family: %v %v", ips, err)
				}
			})
		})
	}
	t.Run("zero_ttl", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := &directDNS{}
			defer d.close()
			ips, err := d.lookupShared(context.Background(), cfexpHost, func(context.Context) ([]string, time.Duration, error) {
				return []string{"104.26.4.28", "2606:4700:20::681a:41c"}, 0, nil
			})
			if err != nil || len(ips) != 2 || len(d.CurrentAddresses(cfexpHost)) != 0 {
				t.Fatal("zero TTL membership boundary changed")
			}
		})
	})
}
