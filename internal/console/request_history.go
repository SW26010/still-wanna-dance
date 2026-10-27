package console

import (
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) serveRequestHistory(w http.ResponseWriter, r *http.Request) {
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
	root := libraryPaths(c.settings).DatabasePath
	c.mu.Unlock()
	var result any
	var err error
	if r.URL.Path == "/api/latency" {
		result, err = cacheproxy.ReadFirstBodyLatency(r.Context(), root, time.Now())
	} else if r.URL.Path == "/api/response-sources" {
		result, err = cacheproxy.ReadResponseSources(r.Context(), root)
	} else {
		result, err = cacheproxy.ReadRequestHistory(r.Context(), root, q)
	}
	if err != nil {
		http.Error(w, "请求历史读取失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (c *Console) serveRecentRequests(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > cacheproxy.MaxRecentRequests {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	// The active storage root is fixed for this Console. The reader owns
	// its read-only connection and does not borrow the engine's lifetime.
	c.mu.Lock()
	database := c.settings.DatabasePath
	c.mu.Unlock()
	result, err := cacheproxy.ReadRecentRequests(r.Context(), database, limit)
	if err != nil {
		http.Error(w, "最近请求读取失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
