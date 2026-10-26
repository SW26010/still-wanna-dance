package console

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"still-wanna-dance/internal/cacheproxy"
)

type previewCapability struct {
	StorageID string `json:"storageID"`
	cacheproxy.PreviewTarget
}

func (c *Console) previewSignature(payload string) []byte {
	mac := hmac.New(sha256.New, []byte(c.previewKey))
	mac.Write([]byte("library-preview\n" + payload))
	return mac.Sum(nil)
}

func (c *Console) preparePreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		StorageID string `json:"storageID"`
		cacheproxy.DownloadTarget
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "无效预览请求", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	paths, closing := libraryPaths(c.settings), c.closing
	c.mu.Unlock()
	if closing || input.StorageID != cacheproxy.CacheStorageID(paths) {
		http.Error(w, "程序或存储位置已变化，请刷新曲库", http.StatusConflict)
		return
	}
	store, err := c.businessStore()
	var target cacheproxy.PreviewTarget
	if err == nil {
		target, err = store.PreparePreview(r.Context(), paths, input.DownloadTarget)
	}
	if err != nil {
		previewError(w, err)
		return
	}
	data, err := json.Marshal(previewCapability{StorageID: input.StorageID, PreviewTarget: target})
	if err != nil {
		previewError(w, err)
		return
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	query := url.Values{"target": {payload}, "signature": {base64.RawURLEncoding.EncodeToString(c.previewSignature(payload))}}
	writeJSON(w, struct {
		URL string `json:"url"`
	}{"/media/preview?" + query.Encode()})
}

func (c *Console) servePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// A valid capability is insufficient for cross-origin embedding (including
	// other localhost ports). Native media sends Fetch Metadata, not our API token.
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" ||
		r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["target"]) != 1 || len(q["signature"]) != 1 || len(q.Get("target")) > 2048 {
		http.Error(w, "invalid media capability", http.StatusForbidden)
		return
	}
	payload := q.Get("target")
	signature, err := base64.RawURLEncoding.DecodeString(q.Get("signature"))
	if err != nil || c.previewKey == "" || !hmac.Equal(signature, c.previewSignature(payload)) {
		http.Error(w, "invalid media capability", http.StatusForbidden)
		return
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	var capability previewCapability
	if err != nil || json.Unmarshal(data, &capability) != nil {
		http.Error(w, "invalid media capability", http.StatusForbidden)
		return
	}
	c.mu.Lock()
	paths, closing := libraryPaths(c.settings), c.closing
	c.mu.Unlock()
	if closing || capability.StorageID != cacheproxy.CacheStorageID(paths) {
		http.Error(w, "程序或存储位置已变化，请重新预览", http.StatusConflict)
		return
	}
	store, err := c.businessStore()
	var target cacheproxy.PreviewTarget
	if err == nil {
		target, err = store.PreviewMetadata(r.Context(), paths, capability.PreviewTarget)
	}
	if err != nil {
		previewError(w, err)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.Method == http.MethodHead {
		cacheproxy.PreviewHeaders(w, target)
		w.WriteHeader(http.StatusOK)
		return
	}
	c.lifecycleMu.Lock()
	c.mu.Lock()
	var release func()
	if c.closing || r.Context().Err() != nil {
		err = errors.New("预览已取消")
	} else {
		release, err = c.ensureEngineRead(target.Resource)
	}
	service := c.service
	c.mu.Unlock()
	c.lifecycleMu.Unlock()
	if err != nil {
		previewError(w, err)
		return
	}
	defer release()
	service.ServePreview(w, r, target)
}

func previewError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, cacheproxy.ErrSongBlocked) {
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": err.Error()})
}
