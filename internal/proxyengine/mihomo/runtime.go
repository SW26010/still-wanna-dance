// Package mihomo owns SWD's embedded core and its single managed proxy group.
package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/component/profile/cachefile"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
	"still-wanna-dance/internal/upstreamrequest"
)

const defaultConnections = 160
const runtimeCloseBudget = 3 * time.Second
const ProxyGroup = "SWD"
const automaticGroup = "自动故障切换"

// Config contains immutable connection parameters and a keyed stable identity.
type Config struct {
	ID         string
	Parameters map[string]any
}

var (
	ErrConfig = errors.New("mihomo: unsupported or invalid node configuration")
	ErrDial   = errors.New("mihomo: connection failed")
)

var runtimeStarted atomic.Bool
var nativeConfigMu sync.Mutex

type RuntimeOptions struct {
	// Owned application directory; never the subscription's config directory.
	Home        string
	DialTimeout time.Duration
	// A temporary identity must not replace a selection made with the durable key.
	DisableSelectionPersistence bool
}

// Runtime is the only complete Mihomo generation in a process. It cannot be
// restarted after failure/retirement; application restart creates a new one.
type Runtime struct {
	dial                    upstreamrequest.DialFunc
	admission               *admission
	controller              *controller
	closers                 []io.Closer
	selectionCacheAvailable bool // Immutable result of opening the native cache at startup.
	once                    sync.Once
	done                    chan struct{}
}

func NewRuntime(configs []Config, options RuntimeOptions) (*Runtime, error) {
	if len(configs) == 0 || options.Home == "" || !filepath.IsAbs(options.Home) || options.DialTimeout < 0 {
		return nil, ErrConfig
	}
	nodes := make([]map[string]any, 0, len(configs))
	ids := make(map[string]bool)
	var names []string
	for i, cfg := range configs {
		if !upstreamrequest.PersistentProxyID(cfg.ID) || ids[cfg.ID] {
			return nil, ErrConfig
		}
		ids[cfg.ID] = true
		p, err := nativeParameters(cfg.Parameters)
		if err != nil {
			return nil, ErrConfig
		}
		label, _ := cfg.Parameters["name"].(string)
		if label == "" {
			label = fmt.Sprintf("节点 %d", i+1)
		}
		name := label + " [" + cfg.ID[len("stable-v1-"):len("stable-v1-")+12] + "]"
		p["name"] = name
		names = append(names, name)
		nodes = append(nodes, p)
	}
	if !runtimeStarted.CompareAndSwap(false, true) {
		return nil, errors.New("mihomo: runtime already used; restart application")
	}
	if err := os.MkdirAll(options.Home, 0700); err != nil {
		return nil, errors.New("mihomo: runtime directory unavailable")
	}
	nativeConfigMu.Lock()
	defer nativeConfigMu.Unlock()
	C.SetHomeDir(options.Home)
	C.Version = "embedded"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, module := range info.Deps {
			if module.Path == "github.com/metacubex/mihomo" {
				C.Version = module.Version
				break
			}
		}
	}
	data, _ := json.Marshal(runtimeConfig(nodes, names, !options.DisableSelectionPersistence))
	defer clear(data)
	parsed, err := executor.ParseWithBytes(data)
	if err != nil {
		return nil, ErrConfig
	}
	r := &Runtime{done: make(chan struct{})}
	for _, p := range parsed.Proxies {
		// All references, including native providers, share this Proxy. Install
		// guards before ApplyConfig publishes it or restores cached selections.
		if proxy, ok := p.(*adapter.Proxy); ok {
			switch group := proxy.Adapter().(type) {
			case *outboundgroup.Selector:
				proxy.ProxyAdapter = &synchronizedSelector{Selector: group}
			case *outboundgroup.Fallback:
				// The automatic group is never manually pinned. Hide SelectAble so
				// group delay checks cannot mutate its otherwise immutable selected
				// field concurrently with native fallback reads.
				proxy.ProxyAdapter = struct{ outboundgroup.ProxyGroup }{group}
			}
		}
		r.closers = append(r.closers, p)
	}
	for _, p := range parsed.Providers {
		if c, ok := p.(io.Closer); ok {
			r.closers = append(r.closers, c)
		}
	}
	executor.ApplyConfig(parsed, true)
	if cache := cachefile.Cache(); cache.DB != nil {
		r.selectionCacheAvailable = true
		r.closers = append(r.closers, cache)
	} else {
		// Native selection still works in memory when the optional cache is nil.
		slog.Warn("mihomo_selection_cache_unavailable", "path", filepath.Join(options.Home, "cache.db"))
	}
	p := tunnel.Proxies()[ProxyGroup]
	if p == nil {
		_ = r.Close()
		return nil, ErrConfig
	}
	timeout := options.DialTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	r.admission = newAdmission(defaultConnections, timeout)
	r.dial = r.admission.wrap(func(ctx context.Context, _ string, address string) (net.Conn, error) {
		metadata := &C.Metadata{Type: C.INNER, NetWork: C.TCP}
		if err := metadata.SetRemoteAddress(address); err != nil {
			return nil, err
		}
		conn, err := p.DialContext(ctx, metadata)
		if err != nil {
			return nil, err
		}
		return statistic.NewTCPTracker(conn, statistic.DefaultManager, metadata, nil, 0, 0, true), nil
	})
	r.controller, err = startController()
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func runtimeConfig(nodes []map[string]any, names []string, persistSelection bool) map[string]any {
	// Literal-IP DoH avoids bootstrap/system hosts, with normal certificate
	// verification. This is core-owned DNS; SWD's existing directDNS is untouched.
	dns := []string{"https://223.5.5.5/dns-query", "https://1.1.1.1/dns-query"}
	return map[string]any{
		"mode": "rule", "log-level": "silent", "allow-lan": false, "find-process-mode": "off",
		"port": 0, "socks-port": 0, "mixed-port": 0, "redir-port": 0, "tproxy-port": 0,
		"tun": map[string]any{"enable": false}, "sniffer": map[string]any{"enable": false}, "ntp": map[string]any{"enable": false},
		"geo-auto-update": false, "profile": map[string]any{"store-selected": persistSelection, "store-fake-ip": false},
		"dns":     map[string]any{"enable": true, "listen": "", "ipv6": false, "use-hosts": false, "use-system-hosts": false, "enhanced-mode": "redir-host", "default-nameserver": dns, "nameserver": dns, "proxy-server-nameserver": dns},
		"proxies": nodes, "rules": []string{"MATCH,REJECT"},
		"proxy-groups": []map[string]any{
			{"name": "GLOBAL", "type": "select", "proxies": []string{ProxyGroup}, "hidden": true},
			{"name": ProxyGroup, "type": "select", "proxies": append([]string{automaticGroup}, names...)},
			{"name": automaticGroup, "type": "fallback", "proxies": names, "url": "https://www.gstatic.com/generate_204", "interval": 300, "lazy": true},
		},
	}
}

func (r *Runtime) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return r.dial(ctx, network, address)
}

// SelectionCacheAvailable reports whether the native cache opened at startup.
// Later write failures remain subject to the native cache's behavior.
func (r *Runtime) SelectionCacheAvailable() bool {
	return r.selectionCacheAvailable
}

// Retirement is immediate; the budget covers native connection, adapter and
// provider Close AND draining. Native code cannot be force-unloaded. A timeout
// is observable and the process must exit; no replacement runtime is created.
func (r *Runtime) Close() error {
	r.once.Do(func() {
		var pending []<-chan struct{}
		if r.admission != nil {
			pending = append(pending, r.admission.retire())
		}
		if r.controller != nil {
			r.controller.close()
		}
		go func() {
			var wg sync.WaitGroup
			for _, c := range r.closers {
				wg.Add(1)
				go func() { defer wg.Done(); _ = c.Close() }()
			}
			wg.Add(1)
			go func() { defer wg.Done(); executor.Shutdown() }()
			for _, done := range pending {
				<-done
			}
			wg.Wait()
			close(r.done)
		}()
	})
	timer := time.NewTimer(runtimeCloseBudget)
	defer timer.Stop()
	select {
	case <-r.done:
		return nil
	case <-timer.C:
		return errors.New("mihomo: runtime did not drain before exit budget")
	}
}
