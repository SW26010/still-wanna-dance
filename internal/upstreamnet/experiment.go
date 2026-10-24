//go:build cf_block_experiment

package upstreamnet

// These probes support the existing opt-in CF experiments. They are absent
// from product builds and never expose mutable resolver state.
import (
	"context"
	"slices"
	"time"
)

type ExperimentDNSQuery = dnsQuery

const (
	ExperimentDNSA    = dnsTypeA
	ExperimentDNSAAAA = dnsTypeAAAA
)

type ExperimentDOHProvider struct{ Host, IP string }

func ExperimentProviders() []ExperimentDOHProvider {
	var providers []ExperimentDOHProvider
	for _, p := range dohProviders {
		providers = append(providers, ExperimentDOHProvider{p.host, p.ip})
	}
	return providers
}

func ExperimentCollectDNS(ctx context.Context, queries []ExperimentDNSQuery) ([]string, time.Duration, error) {
	return collectDNS(ctx, queries)
}

func (d *Resolver) ExperimentQueryProvider(ctx context.Context, p ExperimentDOHProvider, host string, kind uint16) ([]string, time.Duration, error) {
	return d.queryProvider(ctx, dohProvider{p.Host, p.IP}, host, kind)
}

func (d *Resolver) ExperimentBootstrapProvider(ctx context.Context, host string) ([]string, error) {
	return d.bootstrapProvider(ctx, host)
}

func (d *Resolver) ExperimentLookup(ctx context.Context, host string) ([]string, error) {
	return d.lookup(ctx, host)
}

func (d *Resolver) ExperimentLookupShared(ctx context.Context, host string, query ExperimentDNSQuery) ([]string, error) {
	return d.lookupShared(ctx, host, query)
}

type ExperimentDNSSnapshot struct {
	At                        time.Time
	Host                      string
	CacheIPs                  []string
	CacheUntil                time.Time
	Leases                    map[string]time.Time
	Attempted, Observed, Next time.Time
	LookupPending             bool
	LastError                 string
}

func (d *Resolver) ExperimentSnapshot(host string) ExperimentDNSSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := ExperimentDNSSnapshot{At: time.Now(), Host: host, CacheIPs: slices.Clone(d.cache[host].ips), CacheUntil: d.cache[host].until, Leases: map[string]time.Time{}, LookupPending: d.lookups[host] != nil}
	for ip, until := range d.leases[host] {
		row.Leases[ip] = until
	}
	if s := d.states[host]; s != nil {
		row.Attempted, row.Observed, row.Next = s.attempted, s.observed, s.next
		if s.err != nil {
			row.LastError = "lookup_failed"
		}
	}
	return row
}
