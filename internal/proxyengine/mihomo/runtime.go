// Package mihomo owns SWD's embedded core and its single managed proxy group.
package mihomo

import (
	"context"
	"errors"
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
	"github.com/metacubex/mihomo/config"
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
	// Imported false is retained; a caller may additionally disable persistence.
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
	selectionCache          io.Closer
	once                    sync.Once
	done                    chan struct{}
	selectable              map[string]bool
}

func NewRuntime(data []byte, options RuntimeOptions) (*Runtime, error) {
	if options.Home == "" || !filepath.IsAbs(options.Home) || options.DialTimeout < 0 {
		return nil, ErrConfig
	}
	raw, entry, err := managedConfiguration(data, !options.DisableSelectionPersistence)
	if err != nil {
		return nil, err
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
	parsed, err := config.ParseRawConfig(raw)
	if err != nil {
		return nil, ErrConfig
	}
	r := &Runtime{done: make(chan struct{}), selectable: make(map[string]bool)}
	for name, p := range parsed.Proxies {
		// All references, including native providers, share this Proxy. Install
		// guards before ApplyConfig publishes it or restores cached selections.
		if proxy, ok := p.(*adapter.Proxy); ok {
			switch group := proxy.Adapter().(type) {
			case *outboundgroup.Selector:
				proxy.ProxyAdapter = &synchronizedSelector{Selector: group}
				r.selectable[name] = name != "GLOBAL"
			case *outboundgroup.Fallback:
				// The automatic group is never manually pinned. Hide SelectAble so
				// group delay checks cannot mutate its otherwise immutable selected
				// field concurrently with native fallback reads.
				proxy.ProxyAdapter = struct{ outboundgroup.ProxyGroup }{group}
			case *outboundgroup.URLTest:
				// Native automatic groups keep their automatic policy. Avoid the
				// same unsynchronized manual pin state as native Fallback.
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
	for _, p := range parsed.RuleProviders {
		if c, ok := p.(io.Closer); ok {
			r.closers = append(r.closers, c)
		}
	}
	executor.ApplyConfig(parsed, true)
	if cache := cachefile.Cache(); cache.DB != nil {
		r.selectionCacheAvailable = true
		r.selectionCache = cache
	} else {
		// Native selection still works in memory when the optional cache is nil.
		slog.Warn("Mihomo 节点选择缓存不可用", "event_name", "mihomo_selection_cache_unavailable", "path", filepath.Join(options.Home, "cache.db"))
	}
	p := tunnel.Proxies()[entry]
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
			for _, done := range pending {
				<-done
			}
			wg.Wait()
			// Native shutdown may persist the configured fake-IP pool. Keep its
			// cache open until that native write finishes.
			executor.Shutdown()
			if r.selectionCache != nil {
				_ = r.selectionCache.Close()
			}
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
