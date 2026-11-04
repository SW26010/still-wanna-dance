package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

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
	case r.Method == http.MethodGet && r.URL.Path == "/api/import/space":
		handler = c.serveImportSpace
	case r.Method == http.MethodGet && (r.URL.Path == "/api/logs" || r.URL.Path == "/api/logs/export"):
		handler = c.serveLogs
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
		result := &faultResponseWriter{ResponseWriter: w}
		handler(result, r)
		if !result.reported && r.Context().Err() == nil {
			source := "request:" + r.Method + ":" + r.URL.Path
			if result.status >= 400 && result.status != http.StatusNotFound && result.status != http.StatusMethodNotAllowed {
				var body struct {
					Error string `json:"error"`
					Code  string `json:"code"`
				}
				_ = json.Unmarshal(result.failure.Bytes(), &body)
				if body.Code == "uac_canceled" {
					return
				}
				message := body.Error
				if message == "" {
					message = strings.TrimSpace(result.failure.String())
				}
				if message == "" {
					message = http.StatusText(result.status)
				}
				var failure error = errors.New(message)
				if result.cause != nil {
					failure = &responseFault{message: message, cause: result.cause}
				}
				c.reportFault(source, failure)
			} else if result.status < 400 {
				c.reportFault(source, nil)
			}
		}
	}
}

// Only failed responses are retained, and only up to a small diagnostic bound.
// Unwrap keeps ResponseController's exit acknowledgement and deadlines intact.
type faultResponseWriter struct {
	http.ResponseWriter
	status   int
	failure  bytes.Buffer
	cause    error
	reported bool
}

// Preserve the actual error for incident matching while keeping the existing
// response text as the message shown if browser opening itself fails.
type responseFault struct {
	message string
	cause   error
}

func (e *responseFault) Error() string { return e.message }
func (e *responseFault) Unwrap() error { return e.cause }

func preserveResponseFault(w http.ResponseWriter, err error) {
	if result, ok := w.(*faultResponseWriter); ok {
		result.cause = err
	}
}

func (w *faultResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *faultResponseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *faultResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= 400 && w.failure.Len() < 4096 {
		_, _ = w.failure.Write(data[:min(len(data), 4096-w.failure.Len())])
	}
	return w.ResponseWriter.Write(data)
}

func (w *faultResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Terms actions share this check while remaining available before consent.
func (c *Console) allowManagement(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", http.StatusForbidden)
		return false
	}
	return true
}
