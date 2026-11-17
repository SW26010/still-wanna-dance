package playbackmodel

import (
	"net/http"
	"time"
)

// Admission is a temporary pre-demand seam. describe must only inspect existing
// metadata, block readiness and request history; it must not acquire a demand.
// The ordinary handler is called unchanged unless the model actually rejects.
func Admission(model *Model, now func() time.Time, describe func(*http.Request) (Media, *Transfer, Request, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v, p, q, ok := describe(r)
			if ok && model.Admit(now(), v, p, q).Reject {
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
