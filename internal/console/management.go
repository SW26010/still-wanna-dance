package console

import "net/http"

// Ordinary management handlers run only after the shared token/origin check.
// Public reads, terms admission, media capabilities, SSE and WebSocket are
// dispatched by ServeHTTP before reaching this group.
func (c *Console) serveManagement(w http.ResponseWriter, r *http.Request) {
	var handler http.HandlerFunc
	switch {
	case r.URL.Path == "/api/cache/delete" || r.URL.Path == "/api/cache/open":
		handler = c.cacheAPI
	case r.URL.Path == "/api/songs":
		handler = c.songPolicyAPI
	case r.URL.Path == "/api/library":
		handler = c.libraryAPI
	case r.Method == http.MethodGet && r.URL.Path == "/api/downloads":
		handler = c.serveDownloads
	case r.Method == http.MethodGet && (r.URL.Path == "/api/history" || r.URL.Path == "/api/latency" || r.URL.Path == "/api/response-sources"):
		handler = c.serveRequestHistory
	case r.Method == http.MethodGet && r.URL.Path == "/api/requests":
		handler = c.serveRecentRequests
	case r.Method == http.MethodPost:
		handler = c.serveAction
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if c.allowManagement(w, r) {
		handler(w, r)
	}
}

// Terms actions share this check while remaining available before consent.
func (c *Console) allowManagement(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", http.StatusForbidden)
		return false
	}
	return true
}
