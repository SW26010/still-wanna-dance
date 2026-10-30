package console

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"still-wanna-dance/internal/dashboard"
)

func (c *Console) serveDashboard(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	var proxy *mihomoTransport
	if c.client != nil {
		proxy, _ = c.client.Transport.(*mihomoTransport)
	}
	ready := !c.closing && c.terms.valid() && proxy != nil && proxy.core != nil
	c.mu.Unlock()
	if !ready {
		http.Error(w, "代理面板不可用，请启用内嵌 Mihomo 并重启应用", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodGet && r.URL.Path == "/proxy/" {
		host, port, _ := net.SplitHostPort(c.address)
		q := url.Values{"hostname": {host}, "port": {port}, "protocol": {"http"}, "secondaryPath": {"/api/mihomo"}, "secret": {c.token}, "label": {"SWD"}, "disableUpgradeCore": {"1"}, "disableTunMode": {"1"}}
		target, _ := json.Marshal("/proxy/ui/#/setup?" + q.Encode())
		backend, _ := json.Marshal(map[string]any{"type": "clash", "host": host, "port": port, "protocol": "http", "secondaryPath": "/api/mihomo", "password": c.token, "label": "SWD", "disableUpgradeCore": true, "disableTunMode": true})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Refresh only this managed backend. zashboard otherwise treats the
		// process's new secret as another backend after every application restart.
		fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>代理面板</title><script>
const backend=%s;
try {
  if(localStorage.getItem('config/automatic-disconnection')===null)localStorage.setItem('config/automatic-disconnection','false');
  const key='setup/api-list', list=JSON.parse(localStorage.getItem(key)||'[]');
  const index=list.findIndex(b=>b.host===backend.host&&b.port===backend.port&&b.protocol===backend.protocol&&b.secondaryPath===backend.secondaryPath);
  if(index>=0){list[index]={...backend,uuid:list[index].uuid};localStorage.setItem(key,JSON.stringify(list));}
} catch {}
location.replace(%s);
</script><noscript>请启用 JavaScript 以打开代理面板。</noscript></html>`, backend, target)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/proxy/ui/") {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; worker-src 'self' blob:; connect-src 'self' https: ws://"+c.address+"; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		http.StripPrefix("/proxy/ui", dashboard.Handler).ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/api/mihomo" || strings.HasPrefix(r.URL.Path, "/api/mihomo/") {
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(c.token)) != 1 || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
			http.Error(w, "invalid origin or token", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		http.StripPrefix("/api/mihomo", proxy.core).ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}
