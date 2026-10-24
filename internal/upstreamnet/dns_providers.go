package upstreamnet

import (
	"context"
	"errors"
	"time"
)

type dohProvider struct{ host, ip string }

// Only published production endpoints. An empty IP means the provider domain
// must first be resolved over authenticated DoH, never system/UDP DNS.
var dohProviders = []dohProvider{
	{"dns.alidns.com", "223.5.5.5"},
	{"dns.alidns.com", "223.6.6.6"},
	{"doh.pub", ""},
	{"doh.360.cn", ""},
	{"cloudflare-dns.com", "1.1.1.1"},
	{"dns.google", "8.8.8.8"},
}

func (d *Resolver) queryProvider(ctx context.Context, provider dohProvider, host string, kind uint16) ([]string, time.Duration, error) {
	if provider.ip != "" {
		return d.queryDNS(ctx, provider.host, provider.ip, "/dns-query", host, kind)
	}
	ips, err := d.bootstrapProvider(ctx, provider.host)
	if err != nil {
		return nil, 0, err
	}
	return d.queryProviderIPs(ctx, provider.host, ips, func(ctx context.Context, ip string) ([]string, time.Duration, error) {
		return d.queryDNS(ctx, provider.host, ip, "/dns-query", host, kind)
	})
}

func (d *Resolver) queryProviderIPs(ctx context.Context, provider string, ips []string, query func(context.Context, string) ([]string, time.Duration, error)) ([]string, time.Duration, error) {
	var failures []error
	// Two HTTPS connection attempts at most per dynamically discovered provider.
	for _, ip := range ips[:min(2, len(ips))] {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		addresses, ttl, err := query(attempt, ip)
		cancel()
		// A valid empty answer also confirms the endpoint is working.
		if err == nil {
			return addresses, ttl, nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	// Force an encrypted bootstrap refresh after an endpoint changes or fails.
	d.mu.Lock()
	delete(d.cache, "bootstrap/"+provider)
	d.mu.Unlock()
	return nil, 0, errors.Join(failures...)
}

func (d *Resolver) bootstrapProvider(ctx context.Context, host string) ([]string, error) {
	key := "bootstrap/" + host
	for {
		d.mu.Lock()
		entry := d.cache[key]
		if time.Now().Before(entry.until) {
			d.mu.Unlock()
			return entry.ips, nil
		}
		if d.bootstrapping == nil {
			d.bootstrapping = make(map[string]chan struct{})
		}
		if done := d.bootstrapping[key]; done != nil {
			d.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		d.bootstrapping[key] = done
		d.mu.Unlock()
		var queries []dnsQuery
		for _, p := range dohProviders {
			if p.ip == "" {
				continue
			}
			queries = append(queries, func(ctx context.Context) ([]string, time.Duration, error) {
				return d.queryDNS(ctx, p.host, p.ip, "/dns-query", host, dnsTypeA)
			})
		}
		ips, ttl, err := collectDNS(ctx, queries)
		d.mu.Lock()
		if err == nil {
			if d.cache == nil {
				d.cache = make(map[string]dnsEntry)
			}
			d.cache[key] = dnsEntry{ips, time.Now().Add(ttl)}
		}
		delete(d.bootstrapping, key)
		close(done)
		d.mu.Unlock()
		return ips, err
	}
}
