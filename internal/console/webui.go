package console

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/legal"
)

//go:embed webui.html
var webUIPage string

//go:embed assets/webui/*
var webUIAssets embed.FS

var webUITemplate = template.Must(template.New("webui").Parse(webUIPage))

func (c *Console) serveWebUI(w http.ResponseWriter, page, title string, data map[string]any) {
	if data == nil {
		data = make(map[string]any)
	}
	// Accepted document URLs enter the same app; public help/about remain
	// available before consent without enabling the console's API requests.
	if c.termsAccepted() {
		page = "console"
	}
	data["page"], data["token"] = page, c.token
	if page == "console" || page == "about" {
		data["build"] = buildinfo.Current()
	}
	if page == "console" || page == "help" {
		c.mu.Lock()
		p := c.files()
		data["paths"] = map[string]string{"Config": p.Config, "Root": p.Root, "Storage": c.settings.StorageDir, "Log": p.Log}
		c.mu.Unlock()
	}
	if page == "console" || page == "terms" {
		data["terms"] = map[string]string{"text": legal.Text, "version": legal.Version}
	}
	// Marshal escapes HTML metacharacters including </script> in paths/text.
	encoded, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "page unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = webUITemplate.Execute(w, struct {
		Title, Token string
		Data         template.JS
	}{title, c.token, template.JS(encoded)})
}

func serveWebUIAsset(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/assets/webui/") {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	// Only embedded files are served, with no filesystem fallback.
	content, err := webUIAssets.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		http.NotFound(w, r)
		return true
	}
	w.Write(content)
	return true
}
