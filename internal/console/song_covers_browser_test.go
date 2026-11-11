package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestSongCoverBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_COVERS_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_COVERS_BROWSER_EVIDENCE for isolated real Console and Edge validation")
	}
	c := testConsole(t)
	body := coverJPEG(t)
	var calls atomic.Int32
	var offline atomic.Bool
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "aya.kiva.moe" || !strings.HasPrefix(r.URL.Path, "/images/small") {
			t.Errorf("unexpected upstream %s", r.URL)
		}
		calls.Add(1)
		status, data := 200, body
		if offline.Load() || r.URL.Path == "/images/small2.jpg" {
			status, data = 404, []byte("missing")
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	key := strings.Repeat("a", 32)
	names := []string{"封面联调歌曲一", "封面联调歌曲二", "封面联调歌曲三"}
	artist, dancer := "联调歌手", "联调舞者"
	var songs []cacheproxy.CatalogSong
	for i := range 3 {
		songs = append(songs, cacheproxy.CatalogSong{ID: int64(i + 1), Name: &names[i], Artist: &artist, Dancer: &dancer, MD5: key})
	}
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Source: "test", Revision: "20261111000000", Songs: songs}); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test/cover-state" {
			json.NewEncoder(w).Encode(map[string]any{"requests": calls.Load(), "engine": c.service != nil})
			return
		}
		if r.URL.Path == "/test/offline" {
			offline.Store(true)
			w.WriteHeader(204)
			return
		}
		c.ServeHTTP(w, r)
	}))
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "library-covers-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	for _, id := range []string{"1", "3"} {
		data, err := os.ReadFile(filepath.Join(c.files().Root, "data", "covers", id+".jpg"))
		if err != nil || !bytes.Equal(data, body) {
			t.Fatal("cache not retained", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.files().Root, "data", "covers", "2.jpg")); !os.IsNotExist(err) {
		t.Fatal("failed artwork saved", err)
	}
	policies, err := c.store.SongPolicies(t.Context())
	if err != nil || policies[1] != cacheproxy.SongFavorite {
		t.Fatal("preference not persisted", policies, err)
	}
	if c.service != nil || c.httpServer != nil || c.queue.snapshot().Running {
		t.Fatal("artwork started video/game work")
	}
}
