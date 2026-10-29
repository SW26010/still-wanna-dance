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
		return c.mihomoNetworkFor(s, defaultActiveProxyLimit)
	}
	dial := upstreamDialFunc(c.dns.DialContext)
	var proxyDial upstreamrequest.DialFunc
	var proxyID string
	mode := channelMode(s.UpstreamMode)
	if mode == "socks5" {
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
		if s.proxySecretError == "" {
			proxyID, err = c.proxyIdentity(s)
		}
		if err != nil {
			return nil, nil, err
		}
		dial = upstreamDialFunc(proxyDial)
	}
	pool, err := upstreamrequest.NewPool(mode, c.dns, proxyDial, proxyID)
	if err != nil {
		return nil, nil, err
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &upstreamrequest.Transport{Pool: pool, RetainedProxies: c.retainedProxyNodes(s, proxyID), RoundTripper: &http.Transport{
			DialContext: dial, ResponseHeaderTimeout: 20 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return dial, client, nil
}

// Persisted channel keys retain their existing prefix independently of UI
// policy names and the implementation used by each fixed outbound.
func channelMode(mode string) string {
	if mode == "proxy" {
		return "socks5"
	}
	return mode
}

// The client belongs to this Console's active configuration. Saving settings
// never replaces it; a new Console loads the next configuration at startup.
func (c *Console) upstreamClient() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}
