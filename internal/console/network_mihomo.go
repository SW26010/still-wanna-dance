package console

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/proxyengine/mihomo"
	"still-wanna-dance/internal/upstreamrequest"
)

const defaultActiveProxyLimit = 5

type savedProxyMembership struct {
	ids   []string
	ready bool
}

func (m savedProxyMembership) KnownProxyIDs() ([]string, bool) { return slices.Clone(m.ids), m.ready }

// Selecting the external backend does not delete the imported node library.
func (c *Console) retainedProxyNodes(s Settings, externalID string) upstreamrequest.ProxyMembership {
	if s.nodeSnapshot == "" && s.nodeSecretError == "" {
		return nil
	}
	m := savedProxyMembership{ready: s.nodeSecretError == ""}
	if externalID != "" {
		m.ids = append(m.ids, externalID)
	}
	key, err := loadProxyIdentityKey(c.files().ChannelKey)
	if err != nil {
		m.ready = false
		return m
	}
	defer clear(key)
	for _, p := range nodeDocument(s.nodeSnapshot).Nodes {
		m.ids = append(m.ids, nodeIdentity(key, p))
	}
	return m
}

// The runtime owns active nodes only. Saved membership is independent of K and
// of whether the current mode grants any proxy business eligibility.
type mihomoTransport struct {
	*upstreamrequest.Transport
	nodes           []*mihomo.Node
	known           []string
	authoritative   bool
	identityWarning string
	once            sync.Once
	retired         atomic.Bool
}

func (t *mihomoTransport) KnownProxyIDs() ([]string, bool) {
	return slices.Clone(t.known), t.authoritative
}
func (t *mihomoTransport) Retire() {
	t.once.Do(func() {
		t.retired.Store(true)
		t.Transport.Retire() // reject new candidate requests first
		for _, n := range t.nodes {
			_ = n.Close()
		}
	})
}

func (c *Console) mihomoNetworkFor(s Settings, limit int) (upstreamDialFunc, *http.Client, error) {
	if limit < 1 {
		return nil, nil, errors.New("活跃节点上限必须大于零")
	}
	runtime := &mihomoTransport{authoritative: s.nodeSecretError == ""}
	key, err := loadProxyIdentityKey(c.files().ChannelKey)
	if err != nil {
		// A fresh process-local random key still meets the adapter fingerprint
		// contract but cannot match any prior run. Preserve existing history.
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, nil, err
		}
		runtime.authoritative = false
		runtime.identityWarning = "通道身份密钥不可用；本次使用临时身份，不复用旧观测"
		slog.Warn("mihomo_channel_identity_unavailable")
	}
	defer clear(key)
	configs := make(map[string]map[string]any)
	for _, p := range nodeDocument(s.nodeSnapshot).Nodes {
		id := nodeIdentity(key, p)
		if _, exists := configs[id]; !exists {
			runtime.known = append(runtime.known, id)
			configs[id] = p
		}
	}
	slices.Sort(runtime.known)
	var proxies []upstreamrequest.Proxy
	if s.UpstreamMode != "direct" {
		// No durable, comparable resource first-response samples exist at boot.
		// This is a stable provisional set, not a claimed global Top K.
		for _, id := range runtime.known[:min(limit, len(runtime.known))] {
			n, err := mihomo.New(mihomo.Config{ID: id, Parameters: configs[id]}, mihomo.Options{LookupIP: func(ctx context.Context, host string) ([]netip.Addr, error) {
				ips, err := c.dns.lookup(ctx, host)
				if err != nil {
					return nil, err
				}
				var out []netip.Addr
				for _, ip := range ips {
					if a, err := netip.ParseAddr(ip); err == nil {
						out = append(out, a)
					}
				}
				return out, nil
			}})
			if err != nil {
				for _, existing := range runtime.nodes {
					_ = existing.Close()
				}
				return nil, nil, errors.New("无法创建已保存代理节点，请重新导入")
			}
			runtime.nodes = append(runtime.nodes, n)
			proxies = append(proxies, n.Proxy())
		}
	}
	pool, err := upstreamrequest.NewMultiPool(channelMode(s.UpstreamMode), c.dns, proxies)
	if err != nil {
		for _, n := range runtime.nodes {
			_ = n.Close()
		}
		return nil, nil, err
	}
	// Raw callers have no measured channel. Bound connection attempts and never
	// dial directly under forced proxy, including an unreadable/empty snapshot.
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if runtime.retired.Load() {
			return nil, net.ErrClosed
		}
		var attempts []upstreamrequest.DialFunc
		if s.UpstreamMode != "proxy" && s.UpstreamMode != "socks5" {
			attempts = append(attempts, c.dns.DialContext)
		}
		for _, proxy := range proxies {
			attempts = append(attempts, proxy.Dial)
		}
		if len(attempts) == 0 {
			return nil, errors.New("没有可用的活跃代理节点，请导入并保存、重启")
		}
		for i, attempt := range attempts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			child, cancel := channelAttempt(ctx, 10*time.Second, len(attempts)-i)
			conn, err := attempt(child, network, address)
			cancel()
			if err == nil {
				return conn, nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("当前允许的上游连接均不可用")
	}
	runtime.Transport = &upstreamrequest.Transport{Pool: pool, RoundTripper: &http.Transport{
		DialContext: dial, ResponseHeaderTimeout: 20 * time.Second, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
	}}
	return dial, &http.Client{Timeout: 30 * time.Second, Transport: runtime, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
