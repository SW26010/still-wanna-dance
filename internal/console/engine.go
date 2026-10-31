package console

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

var errConsoleClosing = errors.New("程序正在退出")

// Task/gateway admission already holds lifecycleMu across its own state change.
// Call without mu: engine I/O never holds the console state lock.
func (c *Console) ensureEngineLocked() (*cacheproxy.Server, error) {
	engine, _, err := c.openEngineLocked("")
	return engine, err
}

// The returned read keeps the engine and resource alive until release, including
// first-use budget reclamation. Neither console lock is held while streaming.
func (c *Console) retainEngineRead(ctx context.Context, key string) (*cacheproxy.Server, func(), error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return c.openEngineLocked(key)
}

// lifecycleMu serializes creation, task admission and shutdown. mu only protects
// publication and observation; callers never lend their state lock to this code.
func (c *Console) openEngineLocked(key string) (*cacheproxy.Server, func(), error) {
	c.mu.Lock()
	engine, settings, closing := c.service, c.settings, c.closing
	c.mu.Unlock()
	if closing {
		return nil, nil, errConsoleClosing
	}
	if engine != nil {
		if key != "" {
			release, err := engine.RetainRead(key)
			if err != nil {
				return nil, nil, err
			}
			return engine, release, nil
		}
		return engine, nil, nil
	}
	store, err := c.businessStore()
	if err != nil {
		return nil, nil, err
	}
	cfg := cacheproxy.DefaultConfig()
	cfg.Work = c.work
	cfg.BeginVideoRequest = c.beginVideoRequest
	cfg.BeginResourceLoad = c.resources.BeginResourceLoad
	cfg.IsResourceHost = c.resources.IsResourceDomain
	cfg.Logger = slog.Default().With("component", "cache")
	cfg.Store = store
	cfg.VideoDir = libraryPaths(settings).VideoDir
	cfg.VideoBudgetBytes = settings.VideoBudgetBytes
	cfg.CacheUnlimited = settings.VideoStorageMode == "unlimited"
	cfg.RetainFavorites = settings.RetainFavorites
	cfg.AggressiveBlockReclamation = settings.AggressiveBlockReclamation
	if settings.VideoStorageMode == "none" {
		cfg.VideoBudgetBytes = 0
	}
	cfg.RequestRetentionDays = settings.RequestRetentionDays
	cfg.RequestRetentionUnlimited = settings.RequestRetentionMode == "unlimited"
	if settings.RequestRetentionMode == "none" {
		cfg.RequestRetentionDays = 0
	}
	cfg.PrepareAhead = time.Duration(settings.PrepareAheadSeconds) * time.Second
	cfg.DownloadConcurrencySoftLimit = settings.DownloadConcurrencySoftLimit
	cfg.DownloadGrowInterval = time.Duration(settings.DownloadGrowIntervalMS) * time.Millisecond
	cfg.LocalProtectionGrace = time.Duration(settings.LocalProtectionGraceSeconds) * time.Second
	cfg.DialContext = c.upstreamDial
	cfg.ResolvePlayback = c.resolvePlayback
	cfg.ResourceTransports = func(target string) []http.RoundTripper {
		var transports []http.RoundTripper
		for _, selection := range c.operationSelections(upstreamstate.Resource, upstreamstate.Constraints{Target: target}) {
			transports = append(transports, selection.Channel.Transport)
		}
		return transports
	}
	var s *cacheproxy.Server
	var release func()
	if key != "" {
		s, release, err = cacheproxy.NewWithRead(cfg, key)
	} else {
		s, err = cacheproxy.New(cfg)
	}
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.service = s
	c.notifyBlockEngineLocked()
	c.mu.Unlock()
	return s, release, nil
}

// Called with lifecycleMu after admission is closed and background tasks drain.
// Server.Close waits for retained reads before the application closes its store.
func (c *Console) closeEngineLocked() error {
	c.mu.Lock()
	engine := c.service
	c.service = nil
	c.mu.Unlock()
	if engine != nil {
		return engine.Close()
	}
	return nil
}

func (c *Console) waitBlockEngine(ctx context.Context) *cacheproxy.Server {
	c.mu.Lock()
	if c.service != nil || c.closing || ctx.Err() != nil {
		service := c.service
		if c.closing || ctx.Err() != nil {
			service = nil
		}
		c.mu.Unlock()
		return service
	}
	wake := make(chan struct{})
	if c.blockEngineWaiters == nil {
		c.blockEngineWaiters = make(map[chan struct{}]bool)
	}
	c.blockEngineWaiters[wake] = true
	c.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-wake:
	}
	c.mu.Lock()
	delete(c.blockEngineWaiters, wake)
	if len(c.blockEngineWaiters) == 0 {
		c.blockEngineWaiters = nil
	}
	service := c.service
	if c.closing || ctx.Err() != nil {
		service = nil
	}
	c.mu.Unlock()
	return service
}

func (c *Console) notifyBlockEngineLocked() {
	for wake := range c.blockEngineWaiters {
		close(wake)
	}
	c.blockEngineWaiters = nil
}
