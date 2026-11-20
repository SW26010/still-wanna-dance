package upstreamnet

import (
	"context"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

const playUdonDanceHost = "play.udon.dance"

var playUdonDanceIPs = [...]string{
	"162.159.146.57",
	"104.17.189.246",
	"104.18.44.195",
	"104.17.215.188",
	"104.17.98.249",
	"104.17.28.204",
	"104.17.122.21",
}

// Addresses discovers DNS answers before returning fixed-IP candidates.
func (d *Resolver) Addresses(ctx context.Context, host string) ([]upstreamrequest.Address, error) {
	if _, err := d.lookup(ctx, host); err != nil {
		return nil, err
	}
	return d.CurrentAddresses(host), nil
}
func (d *Resolver) CurrentAddresses(host string) []upstreamrequest.Address {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	result := d.dnsAddressesLocked(host)
	if host == playUdonDanceHost && d.resourcePresets {
		for _, ip := range playUdonDanceIPs {
			if !slices.ContainsFunc(result, func(a upstreamrequest.Address) bool { return a.IP == ip }) {
				result = append(result, upstreamrequest.Address{IP: ip})
			}
		}
		// Vary the order of tied candidates beyond the attempt limit. The
		// direct pool interleaves address families before callers select routes.
		if len(result) > upstreamrequest.MaxOperationCandidates {
			rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
		}
	}
	return result
}

func (d *Resolver) dnsAddressesLocked(host string) []upstreamrequest.Address {
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
	e := d.cache[host]
	if !time.Now().Before(e.until) {
		return nil
	}
	result := make([]upstreamrequest.Address, 0, len(e.ips))
	for _, ip := range e.ips {
		result = append(result, upstreamrequest.Address{IP: ip, Expires: e.until})
	}
	return result
}
func (d *Resolver) DNSChanged() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	return d.changed
}
func (d *Resolver) changedLocked() {
	if d.changed != nil {
		close(d.changed)
	}
	d.changed = make(chan struct{})
}
