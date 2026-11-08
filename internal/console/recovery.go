package console

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"still-wanna-dance/internal/branding"
	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/legal"
)

// Recovery admits configuration repair without initializing business resources.
type Recovery struct {
	console       *Console
	identity      desktop.Identity
	reason        string
	mu            sync.Mutex // serializes the entire configuration read/modify/write lifetime
	lease         io.Closer
	settingsError string
	closed        bool
	ctx           context.Context
	cancel        context.CancelFunc
	closeErr      error
}

func NewRecovery(address string, identity desktop.Identity, reason string) (*Recovery, error) {
	c, err := newSettingsConsole(address, identity)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Recovery{console: c, identity: identity, reason: reason, ctx: ctx, cancel: cancel}
	r.openSettings()
	return r, nil
}

func (r *Recovery) ShutdownRequested() <-chan struct{} { return r.console.ShutdownRequested() }
func (r *Recovery) RequestExit() {
	// The owner must be able to cancel pending imports/dialogs before waiting
	// for configuration transactions to drain in Close.
	r.console.RequestExit()
}
func (r *Recovery) WebUIConnected() bool { return r.console.WebUIConnected() }

// Recovery has no business readiness to report. Only committed appearance is
// shared with the normal tray; unreadable/invalid preferences follow the system.
func (r *Recovery) DesktopState() desktop.State {
	r.console.mu.Lock()
	theme := r.console.settings.Theme
	r.console.mu.Unlock()
	if theme != "light" && theme != "dark" {
		theme = "system"
	}
	return desktop.State{Unavailable: true, Theme: theme}
}

func (r *Recovery) BeginShutdown() {
	r.cancel()
	r.console.mu.Lock()
	r.console.closing = true
	if r.console.pathPickerCancel != nil {
		r.console.pathPickerCancel()
	}
	r.console.mu.Unlock()
	if r.console.hosts != nil {
		r.console.hosts.cancel()
	}
	r.console.lifecycle.beginShutdown()
}
func (r *Recovery) ShutdownAction() ShutdownAction { return r.console.ShutdownAction() }
func (r *Recovery) Close(ctx context.Context) error {
	r.BeginShutdown()
	r.console.lifecycle.close(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.console.hosts != nil {
		r.console.lifecycleMu.Lock()
		r.closeErr = r.console.hosts.close(r.console.savedSettings.RestoreHostsOnStopOrExit)
		r.console.lifecycleMu.Unlock()
	}
	if r.lease != nil {
		_ = r.lease.Close()
		r.lease = nil
	}
	return r.closeErr
}

// Release the failed application's resources once all recovery pages have been
// absent for the grace period. Reconnecting cancels the pending countdown.
func (r *Recovery) ExitWhenUnattended(ctx context.Context, grace time.Duration) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		clients, changed := r.console.lifecycle.clientSnapshot()
		var expired <-chan time.Time
		if clients == 0 {
			if timer == nil {
				timer = time.NewTimer(grace)
			}
			expired = timer.C
		} else if timer != nil {
			timer.Stop()
			timer = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-expired:
			if r.console.lifecycle.requestExitIfUnattended() {
				return
			}
			timer = nil
		}
	}
}

func (r *Recovery) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c := r.console
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if req.Host != c.address {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if serveWebUIAsset(w, req) {
		return
	}
	settings := c.settingsHandler(req.URL.Path, false)
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/assets/logo.svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(branding.SVG)
	case req.Method == http.MethodGet && (req.URL.Path == "/favicon.ico" || req.URL.Path == "/favicon-light.ico"):
		w.Header().Set("Content-Type", "image/x-icon")
		if req.URL.Path == "/favicon-light.ico" {
			_, _ = w.Write(branding.LightICO)
		} else {
			_, _ = w.Write(branding.ICO)
		}
	case req.Method == http.MethodGet && req.URL.Path == "/terms.txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, legal.Text)
	case req.Method == http.MethodGet && (req.URL.Path == "/" || req.URL.Path == "/help" || req.URL.Path == "/about" || req.URL.Path == "/terms"):
		serveWebUIPage(w, "WebUI", c.token, map[string]any{
			"page": "console", "token": c.token, "theme": r.DesktopState().Theme, "recovery": map[string]string{"reason": r.reason},
			"settingsDefaults": downloadSettingsDefaults(),
			"build":            buildinfo.Current(), "terms": map[string]string{"text": legal.Text, "version": legal.Version},
		})
	case req.Method == http.MethodGet && req.URL.Path == "/api/identity":
		writeJSON(w, r.identity)
	case req.URL.Path == "/api/lifecycle":
		c.serveLifecycle(w, req)
	case req.Method == http.MethodGet && req.URL.Path == "/api/recovery/settings":
		r.serveSettings(w, req, settings)
	case req.Method == http.MethodPost && settings != nil:
		r.serveSettings(w, req, settings)
	case req.Method == http.MethodPost && (req.URL.Path == "/api/exit" || req.URL.Path == "/api/restart"):
		if !c.allowManagement(w, req) {
			return
		}
		action := ShutdownExit
		if req.URL.Path == "/api/restart" && c.ShutdownAction() == "running" {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.allowSettings(w) {
				return
			}
			loaded, err := decodeConfigFile(r.identity.Config)
			if err == nil {
				_, err = c.resolveSettings(loaded.Settings)
			}
			if err != nil {
				if !writeSettingsValidation(w, err) {
					http.Error(w, err.Error(), http.StatusBadRequest)
				}
				return
			}
		}
		if req.URL.Path == "/api/restart" {
			action = ShutdownRestart
		}
		c.lifecycle.request(action, func(action ShutdownAction) {
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(lifecycleWriteTimeout))
			writeJSON(w, struct {
				OK     bool           `json:"ok"`
				Action ShutdownAction `json:"action"`
			}{true, action})
			_ = controller.Flush()
		})
	case req.URL.Path == "/api/status":
		http.Error(w, "程序启动未完成", http.StatusServiceUnavailable)
	default:
		http.Error(w, "程序启动未完成", http.StatusServiceUnavailable)
	}
}
