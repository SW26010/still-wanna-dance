package console

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
)

// Seed candidate membership through the network contract, not resolver internals.
// Unseeded hosts retain the real resolver, including opt-in integration checks.
type fixtureDNS struct {
	directResolver
	mu        sync.Mutex
	addresses map[string][]upstreamrequest.Address
	queries   atomic.Int32
}

func newFixtureDNS() *fixtureDNS {
	return &fixtureDNS{directResolver: upstreamnet.NewResolver(nil), addresses: make(map[string][]upstreamrequest.Address)}
}

func (d *fixtureDNS) seed(host string, ips ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var addresses []upstreamrequest.Address
	for _, ip := range ips {
		addresses = append(addresses, upstreamrequest.Address{IP: ip, Expires: time.Now().Add(time.Hour)})
	}
	d.addresses[host] = addresses
}

func (d *fixtureDNS) CurrentAddresses(host string) []upstreamrequest.Address {
	d.mu.Lock()
	addresses, ok := d.addresses[host]
	d.mu.Unlock()
	if ok {
		return append([]upstreamrequest.Address(nil), addresses...)
	}
	return d.directResolver.CurrentAddresses(host)
}

func (d *fixtureDNS) Addresses(ctx context.Context, host string) ([]upstreamrequest.Address, error) {
	d.queries.Add(1)
	if addresses := d.CurrentAddresses(host); len(addresses) > 0 {
		return addresses, nil
	}
	return d.directResolver.Addresses(ctx, host)
}

func (d *fixtureDNS) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.queries.Add(1)
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	addresses, seeded := d.addresses[host]
	d.mu.Unlock()
	if seeded && len(addresses) > 0 {
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(addresses[0].IP, port))
	}
	return d.directResolver.DialContext(ctx, network, address)
}

func testDNS(c *Console) *fixtureDNS { return c.dns.(*fixtureDNS) }
