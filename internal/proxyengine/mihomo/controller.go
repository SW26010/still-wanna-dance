package mihomo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/metacubex/mihomo/hub/route"
)

// The native API remains private. SWD exposes only the operations appropriate
// for a managed embedded core; it never gives the browser this secret.
type controller struct {
	proxy     *httputil.ReverseProxy
	transport *http.Transport
	ctx       context.Context
	cancel    context.CancelFunc
}

func startController() (*controller, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, err
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(secretBytes)
	target, _ := url.Parse("http://" + address)
	ctx, cancel := context.WithCancel(context.Background())
	c := &controller{ctx: ctx, cancel: cancel, transport: &http.Transport{Proxy: nil}}
	c.proxy = &httputil.ReverseProxy{
		Transport: c.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Set("Authorization", "Bearer "+secret)
			pr.Out.Header.Del("Origin")
			q := pr.Out.URL.Query()
			q.Del("token")
			pr.Out.URL.RawQuery = q.Encode()
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "代理控制接口不可用", http.StatusBadGateway)
		},
	}
	route.SetEmbedMode(true)
	route.ReCreateServer(&route.Config{Addr: address, Secret: secret})
	// Upstream owns its listener and starts it asynchronously. Verify the
	// authenticated endpoint, rather than treating an available port as readiness.
	ready, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	client := &http.Client{Transport: c.transport, Timeout: time.Second}
	for {
		req, _ := http.NewRequestWithContext(ready, http.MethodGet, target.String()+"/version", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return c, nil
			}
			break // Another listener acquired the port; do not publish it.
		}
		select {
		case <-ready.Done():
			c.close()
			return nil, errors.New("mihomo controller did not become ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	c.close()
	return nil, errors.New("mihomo controller authentication failed")
}

func (c *controller) close() {
	c.cancel()
	c.transport.CloseIdleConnections()
	route.ReCreateServer(&route.Config{})
}

// ServeHTTP consumes a native API path. The Console owns browser admission.
// Configuration, upgrades, restarts, raw logs and arbitrary file/storage access
// stay outside the dashboard's authority.
func (r *Runtime) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.controller == nil || r.controller.ctx.Err() != nil {
		http.Error(w, "代理已停止", http.StatusServiceUnavailable)
		return
	}
	p := strings.TrimSuffix(req.URL.Path, "/")
	allowed := false
	if req.Method == http.MethodGet {
		switch p {
		case "", "/version", "/configs", "/traffic", "/memory", "/connections", "/proxies", "/group", "/rules", "/providers/proxies", "/providers/rules":
			allowed = true
		default:
			allowed = strings.HasPrefix(p, "/proxies/") || strings.HasPrefix(p, "/group/") || strings.HasPrefix(p, "/providers/proxies/")
		}
	}
	if req.Method == http.MethodPut && p == "/proxies/"+ProxyGroup {
		allowed = true
	}
	if req.Method == http.MethodDelete && (p == "/connections" || strings.HasPrefix(p, "/connections/")) {
		allowed = true
	}
	if !allowed {
		http.Error(w, "配置与核心由 SWD 管理；请在 SWD 设置中修改", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	stop := context.AfterFunc(r.controller.ctx, cancel)
	defer stop()
	if p == "/memory" {
		serveMemory(w, req.WithContext(ctx))
		return
	}
	r.controller.proxy.ServeHTTP(w, req.WithContext(ctx))
}
