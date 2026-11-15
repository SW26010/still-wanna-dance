package console

import (
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) serveRequestHistoryEvents(w http.ResponseWriter, r *http.Request) {
	if !c.allowEventStream(w, r) {
		return
	}
	c.mu.Lock()
	allowed := !c.closing && c.terms.valid()
	c.mu.Unlock()
	if !allowed {
		http.Error(w, "请先阅读并同意使用条款", http.StatusConflict)
		return
	}
	store, err := c.openedBusinessStore()
	if err != nil {
		http.Error(w, "请求历史不可用，请检查数据库状态", http.StatusServiceUnavailable)
		return
	}
	// Every connection starts with an invalidation, including reconnections.
	// No event replay or history rows are kept in memory for this stream.
	c.serveRealtimeEvents(w, r, "history", func() diagnosticEventSnapshot {
		revision, changed, closed := store.RequestHistoryChanges()
		state := struct {
			Closed bool `json:"closed"`
		}{Closed: closed}
		return diagnosticEventSnapshot{state: state, revision: revision, changed: changed, closed: closed}
	})
}

func (c *Console) serveRequestHistory(w http.ResponseWriter, r *http.Request) {
	q := cacheproxy.RequestQuery{Limit: 50, Cursor: r.URL.Query().Get("cursor"), Sort: r.URL.Query().Get("sort"), Direction: r.URL.Query().Get("direction")}
	if raw := r.URL.Query().Get("hideAdjacentCanceled"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			preserveResponseFault(w, rejectRequest(err))
			http.Error(w, "invalid history filter", http.StatusBadRequest)
			return
		}
		q.HideAdjacentCanceled = value
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{{"from", &q.From}, {"to", &q.To}} {
		if raw := r.URL.Query().Get(field.name); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				preserveResponseFault(w, rejectRequest(err))
				http.Error(w, "invalid time range", http.StatusBadRequest)
				return
			}
			*field.value = n
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			preserveResponseFault(w, rejectRequest(err))
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		q.Limit = n
	}
	if err := q.Validate(); err != nil {
		preserveResponseFault(w, rejectRequest(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A retained write fault does not imply that committed history is unreadable.
	// Let the shared reader report failures from its own read path.
	store, err := c.openedBusinessStore()
	if err != nil {
		preserveResponseFault(w, err)
		http.Error(w, "请求历史读取失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	history := store.History()
	var result any
	if r.URL.Path == "/api/history/stats" {
		var refresh bool
		if raw := r.URL.Query().Get("refresh"); raw != "" {
			var parseErr error
			refresh, parseErr = strconv.ParseBool(raw)
			if parseErr != nil {
				preserveResponseFault(w, rejectRequest(parseErr))
				http.Error(w, "invalid statistics refresh", http.StatusBadRequest)
				return
			}
		}
		result, err = history.Statistics(r.Context(), time.Now(), refresh)
	} else {
		result, err = history.Page(r.Context(), q)
	}
	if err != nil {
		preserveResponseFault(w, err)
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
			preserveResponseFault(w, rejectRequest(err))
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
		preserveResponseFault(w, err)
		http.Error(w, "最近请求读取失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
