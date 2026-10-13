package mihomo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/component/proxydialer"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"
	"still-wanna-dance/internal/upstreamrequest"
)

const defaultConnectionsPerNode = 32
const runtimeCloseBudget = 3 * time.Second

var runtimeStarted atomic.Bool
var nativeConfigMu sync.Mutex

type RuntimeOptions struct {
	// Owned application directory; never the subscription's config directory.
	Home        string
	DialTimeout time.Duration
}

// Runtime is the only complete Mihomo generation in a process. It cannot be
// restarted after failure/retirement; application restart creates a new one.
type Runtime struct {
	proxies    []upstreamrequest.Proxy
	admissions []*admission
	closers    []io.Closer
	once       sync.Once
	done       chan struct{}
}

func NewRuntime(configs []Config, options RuntimeOptions) (*Runtime, error) {
	if len(configs) == 0 || options.Home == "" || !filepath.IsAbs(options.Home) || options.DialTimeout < 0 {
		return nil, ErrConfig
	}
	nodes := make([]map[string]any, 0, len(configs))
	ids := make(map[string]bool)
	for i, cfg := range configs {
		if !upstreamrequest.PersistentProxyID(cfg.ID) || ids[cfg.ID] {
			return nil, ErrConfig
		}
		ids[cfg.ID] = true
		p, err := nativeParameters(cfg.Parameters)
		if err != nil {
			return nil, ErrConfig
		}
		p["name"] = fmt.Sprintf("swd-%d", i)
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
	data, _ := json.Marshal(runtimeConfig(nodes))
	defer clear(data)
	parsed, err := executor.ParseWithBytes(data)
	if err != nil {
		return nil, ErrConfig
	}
	r := &Runtime{done: make(chan struct{})}
	for _, p := range parsed.Proxies {
		r.closers = append(r.closers, p)
	}
	for _, p := range parsed.Providers {
		if c, ok := p.(io.Closer); ok {
			r.closers = append(r.closers, c)
		}
	}
	executor.ApplyConfig(parsed, true)
	for i, cfg := range configs {
		p := tunnel.Proxies()[fmt.Sprintf("swd-%d", i)]
		if p == nil {
			_ = r.Close()
			return nil, ErrConfig
		}
		timeout := options.DialTimeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		a := newAdmission(defaultConnectionsPerNode, timeout)
		r.admissions = append(r.admissions, a)
		// Capture the object once. No name lookup or dynamic group on requests.
		r.proxies = append(r.proxies, upstreamrequest.Proxy{ID: cfg.ID, Dial: a.wrap(proxydialer.New(p, true).DialContext)})
	}
	return r, nil
}

func runtimeConfig(nodes []map[string]any) map[string]any {
	// Literal-IP DoH avoids bootstrap/system hosts, with normal certificate
	// verification. This is core-owned DNS; SWD's existing directDNS is untouched.
	dns := []string{"https://1.1.1.1/dns-query", "https://1.0.0.1/dns-query"}
	return map[string]any{
		"mode": "rule", "log-level": "silent", "allow-lan": false, "find-process-mode": "off",
		"port": 0, "socks-port": 0, "mixed-port": 0, "redir-port": 0, "tproxy-port": 0,
		"tun": map[string]any{"enable": false}, "sniffer": map[string]any{"enable": false}, "ntp": map[string]any{"enable": false},
		"geo-auto-update": false, "profile": map[string]any{"store-selected": false, "store-fake-ip": false},
		"dns":     map[string]any{"enable": true, "listen": "", "ipv6": false, "use-hosts": false, "use-system-hosts": false, "enhanced-mode": "redir-host", "default-nameserver": dns, "nameserver": dns, "proxy-server-nameserver": dns},
		"proxies": nodes, "rules": []string{"MATCH,REJECT"},
	}
}

func (r *Runtime) Proxies() []upstreamrequest.Proxy { return slices.Clone(r.proxies) }

// Retirement is immediate; the budget covers native connection, adapter and
// provider Close AND draining. Native code cannot be force-unloaded. A timeout
// is observable and the process must exit; no replacement runtime is created.
func (r *Runtime) Close() error {
	r.once.Do(func() {
		var pending []<-chan struct{}
		for _, a := range r.admissions {
			pending = append(pending, a.retire())
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
