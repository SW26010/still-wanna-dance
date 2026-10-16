package console

import "net/http"

func (c *Console) serveHelp(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	p := c.files()
	c.serveWebUI(w, "help", "使用帮助", map[string]any{"paths": map[string]string{"Config": p.Config, "Root": p.Root, "Storage": c.settings.StorageDir, "Log": p.Log}})
}
