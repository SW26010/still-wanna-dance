package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestMD5IDOnlyResolutionFailureCountedOnce(t *testing.T) {
	for _, scan := range []bool{true, false} {
		t.Run(fmt.Sprintf("scan=%v", scan), func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			c.settings.DownloadUpstream = "hkg"
			key := fmt.Sprintf("%x", md5.Sum([]byte("cached")))
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Api/Songs/list" {
					fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":1},{"id":2},{"id":3},{"id":1}]}]}}`)
					return
				}
				if r.URL.Query().Get("id") != "2" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Location", fmt.Sprintf("https://nya.xin.moe/files/123/999-version.mp4?e=%s&s=6", key))
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase, c.checksumURL, c.client.Transport = api.URL, "", http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.StorageDir = c.settings.StorageDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			// Failed ID 1 shares a file with successful ID 2; omitted ID 4 survives.
			if err := engine.SyncCatalog(context.Background(), map[string]string{"1": key, "2": key, "4": key}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.StorageDir, "videos", key+".mp4"), []byte("cached"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.startBatchMode(scan); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			done := c.batchDone
			c.mu.Unlock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("batch stalled")
			}
			b := c.batch
			if b.Total != 4 || b.Checked != 4 || b.Failed != 2 || b.Hits != 2 || b.Missing != 0 || b.Downloaded != 0 {
				t.Fatalf("%+v", b)
			}
			failed := map[int64]bool{}
			for _, f := range b.Failures {
				if failed[f.ID] {
					t.Fatalf("duplicate failure: %+v", b)
				}
				failed[f.ID] = true
			}
			if len(failed) != 2 || !failed[1] || !failed[3] {
				t.Fatalf("failures=%v", failed)
			}
			check, release, err := engine.CheckReferences(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if len(check.References) != 3 || check.References["1"] != key || check.References["4"] != key {
				t.Fatalf("%+v", check)
			}
		})
	}
}

func waitMD5Batch(t *testing.T, c *Console, scan bool) {
	t.Helper()
	if err := c.startBatchMode(scan); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch stalled")
	}
	if c.batch.Failed != 0 {
		t.Fatalf("%+v", c.batch)
	}
}

func TestMD5BatchTriesSharedSongIDs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current []int
		working string
		want    string
	}{
		{"prefer_catalog", []int{1, 2}, "2", "2"},
		{"fallback_catalog", []int{1, 2}, "1", "2,1"},
		{"fallback_retained", []int{1}, "99", "1,99"},
		{"all_fail", []int{1, 2}, "", "2,1,99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			c.settings.DownloadUpstream = "cf"
			const body = "shared resource"
			key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
			var downloads atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				fmt.Fprint(w, body)
			}))
			defer origin.Close()
			var mu sync.Mutex
			var attempts []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/catalog" {
					var entries []string
					for _, id := range tc.current {
						entries = append(entries, fmt.Sprintf(`{"id":%d,"checksum":%q}`, id, key))
					}
					fmt.Fprintf(w, `{"code":200,"data":{"time":"revision","groups":[{"entries":[%s]}]}}`, strings.Join(entries, ","))
					return
				}
				id := r.URL.Query().Get("id")
				mu.Lock()
				attempts = append(attempts, id)
				mu.Unlock()
				if id != tc.working {
					http.Error(w, "obsolete song", http.StatusNotFound)
					return
				}
				w.Header().Set("Location", fmt.Sprintf("https://play.udon.dance/files/123/999-version.mp4?e=%s&s=%d", key, len(body)))
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/catalog", http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.StorageDir, cfg.OriginScheme = c.settings.StorageDir, "http"
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			if err := engine.SyncCatalog(context.Background(), map[string]string{"99": key}); err != nil {
				t.Fatal(err)
			}
			if err := c.startBatchMode(false); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			done := c.batchDone
			c.mu.Unlock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("batch stalled")
			}
			mu.Lock()
			got := strings.Join(attempts, ",")
			mu.Unlock()
			if got != tc.want {
				t.Fatalf("resolution order=%s want=%s", got, tc.want)
			}
			b := c.batch
			total := len(tc.current) + 1
			if b.Total != total || b.Checked != total {
				t.Fatalf("%+v", b)
			}
			if tc.working == "" {
				if b.Failed != total || b.Downloaded != 0 || downloads.Load() != 0 {
					t.Fatalf("batch=%+v downloads=%d", b, downloads.Load())
				}
			} else if b.Failed != 0 || b.Downloaded != total || downloads.Load() != 1 {
				t.Fatalf("batch=%+v downloads=%d", b, downloads.Load())
			}
		})
	}
}

func TestMD5CatalogScanAndFillUseSinglePresenceSnapshot(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	const a = "existing content"
	const b = "missing content"
	digest := func(s string) string { return fmt.Sprintf("%x", md5.Sum([]byte(s))) }
	var resolves, downloads, lists atomic.Int32
	var mu sync.Mutex
	mapping := map[int]string{1: a, 2: a, 3: b}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.URL.Query().Get("e") == digest(b) {
			fmt.Fprint(w, b)
		} else {
			fmt.Fprint(w, a)
		}
	}))
	defer origin.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/catalog" {
			lists.Add(1)
			var entries []string
			for id, body := range mapping {
				entries = append(entries, fmt.Sprintf(`{"id":%d,"name":"song","checksum":%q}`, id, digest(body)))
			}
			fmt.Fprintf(w, `{"code":200,"data":{"time":"revision","groups":[{"entries":[%s]}]}}`, strings.Join(entries, ","))
			return
		}
		resolves.Add(1)
		body := b
		w.Header().Set("Location", fmt.Sprintf("https://play.udon.dance/files/123/999-version.mp4?e=%s&s=%d", digest(body), len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/catalog", http.DefaultTransport
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = c.settings.StorageDir
	cfg.OriginScheme = "http"
	for host := range cfg.Origins {
		cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	if err := os.WriteFile(filepath.Join(cfg.StorageDir, "videos", digest(a)+".mp4"), []byte(a), 0600); err != nil {
		t.Fatal(err)
	}
	waitMD5Batch(t, c, true)
	if c.batch.Total != 3 || c.batch.Hits != 2 || c.batch.Missing != 1 || resolves.Load() != 0 || downloads.Load() != 0 {
		t.Fatalf("batch=%+v resolves=%d downloads=%d", c.batch, resolves.Load(), downloads.Load())
	}
	// A fresh catalog adds IDs and updates mappings, retaining IDs omitted by it.
	mu.Lock()
	mapping = map[int]string{2: b, 4: b}
	mu.Unlock()
	waitMD5Batch(t, c, false)
	if c.batch.Total != 4 || c.batch.Hits != 1 || c.batch.Downloaded != 3 || resolves.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("batch=%+v resolves=%d downloads=%d", c.batch, resolves.Load(), downloads.Load())
	}
	waitMD5Batch(t, c, false)
	if c.batch.Hits != 4 || resolves.Load() != 1 || lists.Load() != 3 {
		t.Fatalf("batch=%+v resolves=%d lists=%d", c.batch, resolves.Load(), lists.Load())
	}
	// Unchanged mappings with a missing file still need to be filled again.
	if err := os.Remove(filepath.Join(cfg.StorageDir, "videos", digest(b)+".mp4")); err != nil {
		t.Fatal(err)
	}
	waitMD5Batch(t, c, false)
	if downloads.Load() != 2 || c.batch.Downloaded != 3 {
		t.Fatal(downloads.Load(), c.batch)
	}
}

func TestMD5CatalogInvalidChecksumDoesNotChangeMappings(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"data":{"time":"revision","groups":[{"entries":[{"id":1,"checksum":"bad"}]}]}}`)
	}))
	defer api.Close()
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	<-done
	if !strings.Contains(c.batch.Phase, "无效") || c.batch.Total != 0 {
		t.Fatal(c.batch)
	}
}
