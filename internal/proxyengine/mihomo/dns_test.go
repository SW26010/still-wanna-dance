package mihomo

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"

	"github.com/metacubex/mihomo/component/resolver"
)

func TestDNSContextIsolationAndNoFallback(t *testing.T) {
	initializeDNS()
	var wg sync.WaitGroup
	for _, address := range []string{"192.0.2.1", "192.0.2.2"} {
		ip := netip.MustParseAddr(address)
		ctx := context.WithValue(context.Background(), dnsKey{}, &serverDNS{host: "same.invalid", lookup: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{ip}, nil }})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				ips, err := resolver.ProxyServerHostResolver.LookupIP(ctx, "same.invalid")
				if err != nil || len(ips) != 1 || ips[0] != ip {
					t.Error("cross-node DNS contamination", ips, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, r := range []resolver.Resolver{resolver.DefaultResolver, resolver.SystemResolver, resolver.DirectHostResolver, resolver.ProxyServerHostResolver} {
		if _, err := r.LookupIP(context.Background(), "localhost"); !errors.Is(err, ErrDNS) {
			t.Fatal("implicit DNS fallback", err)
		}
	}
	ctx := context.WithValue(context.Background(), dnsKey{}, &serverDNS{host: "proxy.invalid", lookup: func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("business target sent to proxy resolver")
		return nil, nil
	}})
	if _, err := resolver.ProxyServerHostResolver.LookupIP(ctx, "business.invalid"); !errors.Is(err, ErrDNS) {
		t.Fatal(err)
	}
}
