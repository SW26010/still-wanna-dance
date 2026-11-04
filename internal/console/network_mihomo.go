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
	core         *mihomo.Runtime
	startupError string
	once         sync.Once
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
	t := &mihomoTransport{}
	var err error
	dial := upstreamDialFunc(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("Mihomo 配置不可用")
	})
	// Native health checks begin immediately, even for a lazy fallback group.
	// Keep the endpoint unavailable until the existing terms gate opens.
	if s.nodeSnapshot != "" && c.terms.valid() {
		t.core, err = mihomo.NewRuntime([]byte(s.nodeSnapshot), mihomo.RuntimeOptions{Home: filepath.Join(c.files().StateDir, "mihomo")})
		if err != nil {
			return nil, nil, errors.New("无法启动 Mihomo，请检查配置或依赖资源")
		}
		dial = t.core.DialContext
	}
	t.Transport = &http.Transport{
		DialContext: dial, ResponseHeaderTimeout: 20 * time.Second, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	return dial, &http.Client{Timeout: 30 * time.Second, Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
