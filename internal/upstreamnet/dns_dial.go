package upstreamnet

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"golang.getoutline.org/sdk/transport"
)

// Supply only our independent DNS answers. Outline owns IPv6 preference,
// connection staggering, cancellation and closing losing connections.
func dialResolvedIPs(ctx context.Context, network, address string, ips []string, dialer transport.StreamDialer) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, net.UnknownNetworkError(network)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var addresses []netip.Addr
	for _, text := range ips {
		ip, err := netip.ParseAddr(text)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if network == "tcp4" && !ip.Is4() || network == "tcp6" && !ip.Is6() {
			continue
		}
		addresses = append(addresses, ip)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("DNS returned no addresses for %s", network)
	}
	he := transport.HappyEyeballsStreamDialer{
		Dialer: dialer,
		Resolve: func(context.Context, string) <-chan transport.HappyEyeballsResolution {
			results := make(chan transport.HappyEyeballsResolution, 1)
			results <- transport.HappyEyeballsResolution{IPs: addresses}
			close(results)
			return results
		},
	}
	return he.DialStream(ctx, address)
}
