package upstreamnet

import (
	"context"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/upstreamrequest"
)

const playUdonDanceHost = "play.udon.dance"

var udonCatalogIPs = [...]string{
	"104.21.112.1",
	"104.21.48.1",
	"104.21.96.1",
	"104.21.32.1",
	"104.21.16.1",
	"104.21.64.1",
	"104.17.243.18",
	"2606:4700:20::681a:bde0",
	"2606:4700:20::681a:e7b8",
}

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
	if !d.presets {
		return result
	}
	switch host {
	case playUdonDanceHost:
		result = addPresets(result, playUdonDanceIPs[:], "")
		// Give tied candidates beyond the attempt limit a chance to be used.
		if len(result) > upstreamrequest.MaxOperationCandidates {
			rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
		}
	case catalogmeta.UdonHost:
		result = addPresets(result, udonCatalogIPs[:], "443")
	}
	return result
}

func addPresets(result []upstreamrequest.Address, ips []string, port string) []upstreamrequest.Address {
	for _, ip := range ips {
		if !slices.ContainsFunc(result, func(a upstreamrequest.Address) bool { return a.IP == ip }) {
			result = append(result, upstreamrequest.Address{IP: ip, Port: port})
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
