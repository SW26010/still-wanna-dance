package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSharedResourceCountsBothSongs(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	const body = "shared video"
	if err := os.MkdirAll(filepath.Join(c.settings.StorageDir, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtureVideoPath(c.settings.StorageDir, "138", body), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Api/Songs/list" {
			fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":138},{"id":140}]}]}}`)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/138-abc.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body)))
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()
	defer c.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	for _, name := range []string{"first scan", "repeat scan"} {
		if err := c.startBatchMode(true); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		done := c.batchDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("scan stalled")
		}
		if c.batch.Hits != 2 || c.batch.Checked != 2 || c.batch.Failed != 0 || c.batch.Missing != 0 || c.batch.Reused != 2 {
			t.Fatalf("%s: %+v", name, c.batch)
		}
	}
	inventory := scanInventory(context.Background(), c.settings)
	if inventory.Error != "" || inventory.Videos != 1 || inventory.Bytes != int64(len(body)) {
		t.Fatalf("shared resource counted twice: %+v", inventory)
	}
}
