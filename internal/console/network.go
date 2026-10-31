package console

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
)

type upstreamDialFunc func(context.Context, string, string) (net.Conn, error)

// Console composes the existing DNS pool and dial contracts and owns shutdown.
type directResolver interface {
	upstreamrequest.DNSPool
	DialContext(context.Context, string, string) (net.Conn, error)
	Close()
}

func (c *Console) networkFor(s Settings) (upstreamDialFunc, *http.Client, error) {
	if s.UpstreamMode == "proxy" && s.ProxyBackend == "mihomo" {
		return c.mihomoNetworkFor(s)
	}
	var dial upstreamDialFunc
	var proxyDial upstreamrequest.DialFunc
	if s.UpstreamMode == "proxy" {
		var err error
		if s.proxySecretError != "" {
			proxyDial = func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New(unavailableProxySecret)
			}
		} else {
			proxyDial, err = upstreamnet.NewAuthenticatedSOCKS5Dialer(s.SOCKS5Address, s.SOCKS5Username, s.SOCKS5Password)
		}
		if err != nil {
			return nil, nil, err
		}
		dial = upstreamDialFunc(proxyDial)
	} else {
		if c.dns == nil {
			c.dns = upstreamnet.NewResolver(nil)
		}
		dial = c.dns.DialContext
	}
	var transport http.RoundTripper = &http.Transport{
		DialContext: dial, ResponseHeaderTimeout: 20 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	if s.UpstreamMode != "proxy" {
		pool, err := upstreamrequest.NewPool(c.dns)
		if err != nil {
			return nil, nil, err
		}
		transport = &upstreamrequest.Transport{Pool: pool, RoundTripper: transport}
	}
	client := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return dial, client, nil
}

// The client belongs to this Console's active configuration. Saving settings
// never replaces it; a new Console loads the next configuration at startup.
func (c *Console) upstreamClient() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}
