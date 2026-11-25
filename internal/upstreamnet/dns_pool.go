package upstreamnet

import (
	"context"
	"math/rand/v2"
	"net/url"
	"sort"
	"time"

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/playbacksource"
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

// Playback API and download presets are maintained independently.
var udonPlaybackIPs = [...]string{
	"162.159.146.57",
	"104.17.189.246",
	"104.18.44.195",
	"104.17.215.188",
	"104.17.98.249",
	"104.17.28.204",
	"104.17.122.21",
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
		result = addPresets(result, playUdonDanceIPs[:], nil)
		// Give tied candidates beyond the attempt limit a chance to be used.
		if len(result) > upstreamrequest.MaxOperationCandidates {
			rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
		}
	case catalogmeta.UdonHost:
		result = addPresets(result, udonCatalogIPs[:], func(u *url.URL) bool {
			return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443")
		})
		result = addPresets(result, udonPlaybackIPs[:], func(u *url.URL) bool {
			return u.Scheme == "http" && (u.Port() == "" || u.Port() == "80") && u.EscapedPath() == playbacksource.Path
		})
	}
	return result
}

func addPresets(result []upstreamrequest.Address, ips []string, allows func(*url.URL) bool) []upstreamrequest.Address {
	seen := make(map[string]bool, len(result)+len(ips))
	for _, a := range result {
		// A DNS lease covers every endpoint. A preset for a different
		// endpoint leaves this IP eligible for the list being added.
		if a.Allows == nil {
			seen[a.IP] = true
		}
	}
	for _, ip := range ips {
		if !seen[ip] {
			result = append(result, upstreamrequest.Address{IP: ip, Allows: allows})
			seen[ip] = true
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
