package console

import (
	"context"
	"sort"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// Addresses exposes only valid DNS answers; performance belongs to the monitor.
func (d *directDNS) Addresses(ctx context.Context, host string) ([]upstreamrequest.Address, error) {
	if _, err := d.lookup(ctx, host); err != nil {
		return nil, err
	}
	return d.CurrentAddresses(host), nil
}
func (d *directDNS) CurrentAddresses(host string) []upstreamrequest.Address {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.cache[host]
	if d.closed {
		return nil
	}
	if leases := d.leases[host]; leases != nil {
		var result []upstreamrequest.Address
		for ip, until := range leases {
			if time.Now().Before(until) {
				result = append(result, upstreamrequest.Address{IP: ip, Expires: until})
			}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].IP < result[j].IP })
		return result
	}
	if !time.Now().Before(e.until) {
		return nil
	}
	result := make([]upstreamrequest.Address, 0, len(e.ips))
	for _, ip := range e.ips {
		result = append(result, upstreamrequest.Address{IP: ip, Expires: e.until})
	}
	return result
}
func (d *directDNS) DNSChanged() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	return d.changed
}
func (d *directDNS) changedLocked() {
	if d.changed != nil {
		close(d.changed)
	}
	d.changed = make(chan struct{})
}
