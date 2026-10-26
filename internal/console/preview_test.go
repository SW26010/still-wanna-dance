package console

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func preparePreviewURL(t *testing.T, c *Console, input cacheproxy.DownloadTarget) string {
	t.Helper()
	body, _ := json.Marshal(struct {
		StorageID string `json:"storageID"`
		cacheproxy.DownloadTarget
	}{cacheproxy.CacheStorageID(libraryPaths(c.settings)), input})
	r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/library/preview", strings.NewReader(string(body)))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !strings.HasPrefix(response.URL, "/media/preview?") || strings.Contains(response.URL, c.token) {
		t.Fatal(response, err)
	}
	return response.URL
}

func previewMedia(c *Console, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+c.address+path, nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func TestPreviewPrepareHEADAndInitialReadBeforeEviction(t *testing.T) {
	c := testConsole(t)
	const body = "offline preview in a zero-budget library"
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	path := filepath.Join(c.settings.VideoDir, key+".mp4")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c.upstreamDial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("unexpected network")
		return nil, fmt.Errorf("offline")
	}
	url := preparePreviewURL(t, c, cacheproxy.DownloadTarget{Resource: key})
	w := previewMedia(c, http.MethodHead, url, map[string]string{"Range": "bytes=1-5"})
	if w.Code != 200 || w.Header().Get("Content-Length") != fmt.Sprint(len(body)) || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if c.service != nil || c.httpServer != nil || c.queue.Running || c.batch.Running {
		t.Fatal("preparation/HEAD started work")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("preparation evicted target", err)
	}
	w = previewMedia(c, http.MethodGet, url, map[string]string{"Range": "bytes=1-5"})
	if w.Code != 206 || w.Body.String() != body[1:6] {
		t.Fatal("initial retention deleted preview", w.Code, w.Body.String())
	}
	if c.service == nil || c.httpServer != nil || c.queue.Running || !c.activation.FirstRequest.IsZero() {
		t.Fatal("preview changed game activation", c.activation)
	}
}

func TestPreviewCapabilityAndSongAdmission(t *testing.T) {
	c := testConsole(t)
	key := fmt.Sprintf("%x", md5.Sum([]byte("preview")))
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Source: "test", Revision: "20261027000000", Songs: []cacheproxy.CatalogSong{{ID: 1, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	url := preparePreviewURL(t, c, cacheproxy.DownloadTarget{SongID: "1"})
	for _, tc := range []struct {
		name, method, path string
		headers            map[string]string
		want               int
	}{
		{"metadata", "HEAD", url, nil, 200},
		{"cross-site", "GET", url, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"other port", "GET", url, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:1"}, 403},
		{"forged origin", "GET", url, map[string]string{"Origin": "http://evil.example"}, 403},
		{"navigation", "GET", url, map[string]string{"Sec-Fetch-Site": "none"}, 403},
		{"missing fetch metadata", "GET", url, map[string]string{"Sec-Fetch-Site": ""}, 403},
		{"tampered", "GET", url + "x", nil, 403},
		{"management token", "GET", "/media/preview?token=" + c.token, nil, 403},
		{"write", "POST", url, nil, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := previewMedia(c, tc.method, tc.path, tc.headers)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if c.service != nil {
		t.Fatal("metadata or rejection started engine")
	}
	if err := c.store.SetSongPolicy(t.Context(), 1, cacheproxy.SongBlocked); err != nil {
		t.Fatal(err)
	}
	if w := previewMedia(c, "HEAD", url, nil); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	resource := preparePreviewURL(t, c, cacheproxy.DownloadTarget{Resource: key})
	if w := previewMedia(c, "HEAD", resource, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	keyBefore := c.previewKey
	c.previewKey = "new process key"
	if w := previewMedia(c, "GET", resource, nil); w.Code != 403 {
		t.Fatal("old process signature accepted", w.Code)
	}
	c.previewKey = keyBefore
	c.settings.VideoDir += "-changed"
	if w := previewMedia(c, "GET", resource, nil); w.Code != 409 {
		t.Fatal("stale storage accepted", w.Code)
	}
	if c.service != nil {
		t.Fatal("rejected capability started engine")
	}
}
