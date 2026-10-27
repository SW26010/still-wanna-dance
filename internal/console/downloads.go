package console

import (
	"net/http"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) serveDownloads(w http.ResponseWriter, r *http.Request) {
	// A Go reference keeps the engine object alive, not its database. The
	// snapshot reader handles concurrent Close without joining lifecycle I/O.
	c.mu.Lock()
	service, running := c.service, c.httpServer != nil
	c.mu.Unlock()
	result := struct {
		cacheproxy.ActiveDownloads
		Running bool                   `json:"running"`
		GETs    cacheproxy.CurrentGETs `json:"gets"`
	}{cacheproxy.ActiveDownloads{Tasks: []cacheproxy.ActiveDownload{}}, running, cacheproxy.CurrentGETs{Requests: []cacheproxy.CurrentGET{}}}
	if service != nil {
		var err error
		result.ActiveDownloads, err = service.ActiveDownloads(r.Context())
		result.GETs = service.CurrentGETs()
		c.mu.Lock()
		if c.service != service {
			result.ActiveDownloads = cacheproxy.ActiveDownloads{Tasks: []cacheproxy.ActiveDownload{}}
			result.GETs = cacheproxy.CurrentGETs{Requests: []cacheproxy.CurrentGET{}}
			err = nil // Never publish tasks from a replaced storage engine.
		}
		result.Running = c.httpServer != nil
		c.mu.Unlock()
		if err != nil {
			http.Error(w, "活动下载读取失败，请稍后重试", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, result)
}
