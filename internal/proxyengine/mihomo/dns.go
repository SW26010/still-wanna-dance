package mihomo

import (
	"context"
	"net/netip"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	"github.com/miekg/dns"
)

type dnsKey struct{}
type serverDNS struct {
	host   string
	lookup LookupIP
}
type controlledDNS struct{ proxy bool }

var dnsOnce sync.Once

// All production Mihomo access must go through this package. The process-wide
// globals are installed once, never swapped per node/request or on retirement.
func initializeDNS() {
	dnsOnce.Do(func() {
		resolver.ProxyServerHostResolver = controlledDNS{proxy: true}
		resolver.DefaultResolver = controlledDNS{}
		resolver.SystemResolver = controlledDNS{}
		resolver.DirectHostResolver = controlledDNS{}
		resolver.DefaultHosts = resolver.NewHosts(trie.New[resolver.HostValue]())
		resolver.UseSystemHosts = false
		resolver.DisableSystemHosts = true
		resolver.DisableIPv6 = false
	})
}
func (s *serverDNS) lookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || !strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(s.host, ".")) {
		return nil, ErrDNS
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	ips, err := s.lookup(ctx, host)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrDNS
	}
	var out []netip.Addr
	for _, ip := range ips {
		if ip.IsValid() && ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast() {
			out = append(out, ip.Unmap())
		}
	}
	if len(out) == 0 {
		return nil, ErrDNS
	}
	return out, nil
}
func (r controlledDNS) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if !r.proxy {
		return nil, ErrDNS
	}
	s, _ := ctx.Value(dnsKey{}).(*serverDNS)
	return s.lookupIP(ctx, host)
}
func (r controlledDNS) family(ctx context.Context, host string, v4 bool) ([]netip.Addr, error) {
	ips, err := r.LookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ip := range ips {
		if ip.Is4() == v4 {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, ErrDNS
	}
	return out, nil
}
func (r controlledDNS) LookupIPv4(c context.Context, h string) ([]netip.Addr, error) {
	return r.family(c, h, true)
}
func (r controlledDNS) LookupIPv6(c context.Context, h string) ([]netip.Addr, error) {
	return r.family(c, h, false)
}
func (controlledDNS) ResolveECH(context.Context, string) ([]byte, error)          { return nil, ErrDNS }
func (controlledDNS) ExchangeContext(context.Context, *dns.Msg) (*dns.Msg, error) { return nil, ErrDNS }
func (controlledDNS) Invalid() bool                                               { return true }
func (controlledDNS) ClearCache()                                                 {}
func (controlledDNS) ResetConnection()                                            {}
