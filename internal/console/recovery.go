package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"still-wanna-dance/internal/branding"
	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/legal"
)

// Recovery serves the existing WebUI without loading configuration, opening a
// database or admitting business actions. It is used only after startup fails.
type Recovery struct {
	console  *Console
	identity desktop.Identity
}

func NewRecovery(address string, identity desktop.Identity) (*Recovery, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	c := &Console{address: address, token: hex.EncodeToString(token[:]), lifecycle: newApplicationLifecycle()}
	return &Recovery{console: c, identity: identity}, nil
}

func (r *Recovery) ShutdownRequested() <-chan struct{} { return r.console.ShutdownRequested() }
func (r *Recovery) RequestExit()                       { r.console.RequestExit() }
func (r *Recovery) WebUIConnected() bool               { return r.console.WebUIConnected() }
func (r *Recovery) BeginShutdown()                     { r.console.lifecycle.beginShutdown() }
func (r *Recovery) Close(ctx context.Context)          { r.console.lifecycle.close(ctx) }

// Without a tray there is no way to reopen a closed page. Release this
// isolated server after its last browser session has been absent for a while.
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
			"page": "console", "token": c.token, "theme": "system", "recovery": true,
			"build": buildinfo.Current(), "terms": map[string]string{"text": legal.Text, "version": legal.Version},
		})
	case req.Method == http.MethodGet && req.URL.Path == "/api/identity":
		writeJSON(w, r.identity)
	case req.URL.Path == "/api/lifecycle":
		c.serveLifecycle(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/api/exit":
		if !c.allowManagement(w, req) {
			return
		}
		c.lifecycle.request(ShutdownExit, func(action ShutdownAction) {
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
