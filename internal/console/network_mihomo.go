package console

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"still-wanna-dance/internal/proxyengine/mihomo"
)

// SWD sees one proxy endpoint; Mihomo alone owns the underlying node choice.
type mihomoTransport struct {
	*http.Transport
	core            *mihomo.Runtime
	identityWarning string
	startupError    string
	once            sync.Once
}

func (t *mihomoTransport) Retire() {
	t.once.Do(func() {
		t.Transport.CloseIdleConnections()
		if t.core != nil {
			if err := t.core.Close(); err != nil {
				slog.Warn("mihomo_runtime_drain_incomplete")
			}
		}
	})
}

func (c *Console) mihomoNetworkFor(s Settings) (upstreamDialFunc, *http.Client, error) {
	var configs []mihomo.Config
	key, persistent, err := c.channelIdentityKey()
	if err != nil {
		return nil, nil, err
	}
	defer clear(key)
	for _, p := range nodeDocument(s.nodeSnapshot).Nodes {
		configs = append(configs, mihomo.Config{ID: nodeIdentity(key, p), Parameters: p})
	}
	t := &mihomoTransport{}
	if !persistent && len(configs) > 0 {
		t.identityWarning = "通道身份密钥不可用；本次的节点选择不会保存，请恢复密钥后重启"
	}
	dial := upstreamDialFunc(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("代理节点不可用，请导入并保存、重启")
	})
	// Native health checks begin immediately, even for a lazy fallback group.
	// Keep the endpoint unavailable until the existing terms gate opens.
	if len(configs) > 0 && c.terms.valid() {
		t.core, err = mihomo.NewRuntime(configs, mihomo.RuntimeOptions{Home: filepath.Join(c.files().StateDir, "mihomo"), DisableSelectionPersistence: !persistent})
		if err != nil {
			return nil, nil, errors.New("无法启动代理运行时，请检查节点并重启应用")
		}
		dial = t.core.DialContext
	}
	t.Transport = &http.Transport{
		DialContext: dial, ResponseHeaderTimeout: 20 * time.Second, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	return dial, &http.Client{Timeout: 30 * time.Second, Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
