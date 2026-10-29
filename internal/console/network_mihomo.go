package console

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
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
	key, persistent, err := c.channelIdentityKey()
	defer clear(key)
	if err != nil || !persistent {
		m.ready = false
		return m
	}
	for _, p := range nodeDocument(s.nodeSnapshot).Nodes {
		m.ids = append(m.ids, nodeIdentity(key, p))
	}
	return m
}

// The runtime owns active nodes only. Saved membership is independent of K and
// of whether the current mode grants any proxy business eligibility.
type mihomoTransport struct {
	*upstreamrequest.Transport
	core            *mihomo.Runtime
	proxies         []upstreamrequest.Proxy
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
		if t.core != nil {
			if err := t.core.Close(); err != nil {
				slog.Warn("mihomo_runtime_drain_incomplete")
			}
		}
	})
}

func (c *Console) mihomoNetworkFor(s Settings, limit int) (upstreamDialFunc, *http.Client, error) {
	if limit < 1 {
		return nil, nil, errors.New("活跃节点上限必须大于零")
	}
	runtime := &mihomoTransport{authoritative: s.nodeSecretError == ""}
	key, persistent, err := c.channelIdentityKey()
	if err != nil {
		return nil, nil, err
	}
	if !persistent {
		// The instance's temporary key meets the adapter fingerprint contract
		// but cannot match prior runs. Preserve existing history.
		runtime.authoritative = false
		runtime.identityWarning = "通道身份密钥不可用；本次使用临时身份，不复用旧观测"
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
	// No durable, comparable resource first-response samples exist at boot.
	// This is a stable provisional set, not a claimed global Top K.
	var active []mihomo.Config
	for _, id := range runtime.known[:min(limit, len(runtime.known))] {
		active = append(active, mihomo.Config{ID: id, Parameters: configs[id]})
	}
	if len(active) > 0 {
		core, err := mihomo.NewRuntime(active, mihomo.RuntimeOptions{Home: filepath.Join(c.files().StateDir, "mihomo")})
		if err != nil {
			return nil, nil, errors.New("无法启动代理运行时，请检查节点并重启应用")
		}
		runtime.core, proxies = core, core.Proxies()
	}
	runtime.proxies = proxies
	pool, err := upstreamrequest.NewMultiPool("socks5", nil, proxies)
	if err != nil {
		if runtime.core != nil {
			_ = runtime.core.Close()
		}
		return nil, nil, err
	}
	// Raw callers have no measured channel. Bound attempts to the active proxies,
	// including an unreadable/empty snapshot; target DNS belongs to each proxy.
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if runtime.retired.Load() {
			return nil, net.ErrClosed
		}
		if len(proxies) == 0 {
			return nil, errors.New("没有可用的活跃代理节点，请导入并保存、重启")
		}
		for i, proxy := range proxies {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			child, cancel := channelAttempt(ctx, 10*time.Second, len(proxies)-i)
			conn, err := proxy.Dial(child, network, address)
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
