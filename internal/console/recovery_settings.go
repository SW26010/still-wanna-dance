package console

import (
	"context"
	"errors"
	"maps"
	"net/http"

	"still-wanna-dance/internal/desktop"
)

func (r *Recovery) openSettings() {
	if r.identity.Config == "" || r.identity.Executable == "" {
		r.settingsError = "无法确认程序或配置文件位置，不能修改设置。"
		return
	}
	lease, err := desktop.LockConfig(r.identity.Config)
	if err != nil {
		r.settingsError = err.Error()
		return
	}
	r.lease = lease
	loaded, err := decodeConfigFile(r.identity.Config)
	if err != nil {
		r.settingsError = err.Error()
		return
	}
	// Reserve the original locations for the ordinary settings handlers. This
	// baseline is never exposed as a running application snapshot.
	r.console.initializeSettings(loaded.Settings)
}

func (r *Recovery) allowSettings(w http.ResponseWriter) bool {
	if r.closed || r.ctx.Err() != nil || r.console.ShutdownAction() != "running" {
		http.Error(w, "程序正在退出", http.StatusServiceUnavailable)
		return false
	}
	if r.settingsError != "" || r.lease == nil {
		http.Error(w, "不能修改配置："+r.settingsError, http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (r *Recovery) settingsSnapshot() map[string]any {
	c := r.console
	c.mu.Lock()
	s := c.savedSettings
	nodes := c.proxyNodesStatusLocked()
	actionErrors := maps.Clone(c.actionErrors)
	c.mu.Unlock()
	public := s
	public.SOCKS5Password = ""
	result := map[string]any{
		"settings": public, "restartRequired": true,
		"defaultDatabasePath": c.defaultDatabasePath(), "defaultVideoDir": c.defaultVideoDir(),
		"defaultBlockCacheDir": defaultBlockCacheDir(c.identity.Executable), "portableDir": c.portableDir(), "defaultLogDir": defaultLogDir(),
		"socks5CredentialError": s.proxySecretError, "proxyNodes": nodes, "actionErrors": actionErrors,
	}
	if _, err := c.resolveSettings(s); err != nil {
		var validation *settingsValidationError
		if errors.As(err, &validation) {
			result["fields"] = validation.Fields
		}
	}
	return result
}

func (r *Recovery) serveSettings(w http.ResponseWriter, req *http.Request, handler http.HandlerFunc) {
	c := r.console
	if !c.allowManagement(w, req) {
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(r.ctx, cancel)
	defer cancel()
	defer stop()
	req = req.WithContext(ctx)
	r.mu.Lock()
	if req.Method == http.MethodGet && r.settingsError != "" {
		defer r.mu.Unlock()
		writeJSON(w, map[string]any{"unavailable": r.settingsError})
		return
	}
	if !r.allowSettings(w) {
		r.mu.Unlock()
		return
	}
	loaded, err := decodeConfigFile(r.identity.Config)
	if err != nil {
		r.mu.Unlock()
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	c.mu.Lock()
	c.savedSettings = loaded.Settings
	c.mu.Unlock()
	if req.Method == http.MethodGet {
		defer r.mu.Unlock()
		writeJSON(w, r.settingsSnapshot())
		return
	}
	if req.URL.Path == "/api/path/select" {
		// Native dialogs must be cancelable by shutdown while still open.
		r.mu.Unlock()
		handler(w, req)
		return
	}
	defer r.mu.Unlock()
	handler(w, req)
}
