package console

import (
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) serveRequestHistory(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", http.StatusForbidden)
		return
	}
	q := cacheproxy.RequestQuery{Limit: 50, Cursor: r.URL.Query().Get("cursor")}
	for _, field := range []struct {
		name  string
		value *int64
	}{{"from", &q.From}, {"to", &q.To}} {
		if raw := r.URL.Query().Get(field.name); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				http.Error(w, "invalid time range", http.StatusBadRequest)
				return
			}
			*field.value = n
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		q.Limit = n
	}
	if err := q.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Read the active root once; database I/O must not join service lifecycle I/O.
	c.mu.Lock()
	root := c.settings.StorageDir
	c.mu.Unlock()
	var result any
	var err error
	if r.URL.Path == "/api/latency" {
		result, err = cacheproxy.ReadFirstBodyLatency(r.Context(), root, time.Now())
	} else {
		result, err = cacheproxy.ReadRequestHistory(r.Context(), root, q)
	}
	if err != nil {
		http.Error(w, "请求历史读取失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
