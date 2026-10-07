package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestCacheSearchLengthMatchesHTMLMaxlength(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		name, query string
		status      int
	}{
		{"ASCII boundary", strings.Repeat("a", 300), 200},
		{"ASCII too long", strings.Repeat("a", 301), 400},
		{"Chinese regression", strings.Repeat("舞", 101), 200},
		{"Chinese boundary", strings.Repeat("舞", 300), 200},
		{"Chinese too long", strings.Repeat("舞", 301), 400},
		{"supplementary boundary", strings.Repeat("😀", 150), 200},
		{"supplementary too long", strings.Repeat("😀", 151), 400},
		{"mixed boundary", strings.Repeat("舞", 298) + "😀", 200},
		{"mixed too long", strings.Repeat("舞", 299) + "😀", 400},
		{"invalid UTF-8", "\xff", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+c.address+"/api/cache?q="+url.QueryEscape(tc.query), nil)
			r.Header.Set("X-StepStash-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want=%d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestCacheAPIAuthAndOfflineRead(t *testing.T) {
	c := testConsole(t)
	for _, path := range []string{"/api/cache", "/api/cache/delete", "/api/cache/open"} {
		method := "POST"
		if path == "/api/cache" {
			method = "GET"
		}
		for _, tc := range []struct{ host, token, origin string }{{c.address, "", ""}, {"evil.example", c.token, ""}, {c.address, c.token, "http://evil.example"}} {
			r := httptest.NewRequest(method, "http://"+tc.host+path, strings.NewReader(`{}`))
			r.Header.Set("X-StepStash-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(path, w.Code)
			}
		}
	}
	for _, tc := range []struct {
		path string
		code int
	}{{"/api/cache", 200}, {"/api/cache?offset=-1", 400}, {"/api/cache?sort=invalid", 400}} {
		r := httptest.NewRequest("GET", "http://"+c.address+tc.path, nil)
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("read started service")
	}
}

func TestCacheGETDoesNotAcquireLifecycleLock(t *testing.T) {
	c := testConsole(t)
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/cache", nil)
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		done <- w
	}()
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("cache GET waited for lifecycle lock")
	}
}

func TestCacheSnapshotSlowReadDoesNotBlockLifecycleAndRejectsStaleResults(t *testing.T) {
	for _, operation := range []string{"stop", "close", "switch", "switch-back"} {
		t.Run(operation, func(t *testing.T) {
			c := testConsole(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			type outcome struct {
				page cacheproxy.CachePage
				err  error
			}
			readDone := make(chan outcome, 1)
			go func() {
				page, err := c.cacheSnapshot(func(root string) (cacheproxy.CachePage, error) {
					close(entered)
					<-release
					return cacheproxy.CachePage{StorageID: "old", Entries: []cacheproxy.CacheEntry{{Key: "old"}}}, nil
				})
				readDone <- outcome{page, err}
			}()
			<-entered
			actionDone := make(chan error, 1)
			go func() {
				switch operation {
				case "stop":
					actionDone <- c.stop()
				case "close":
					actionDone <- c.Close()
				default:
					c.mu.Lock()
					original := c.settings
					c.mu.Unlock()
					next := original
					next.StorageDir = filepath.Join(t.TempDir(), "other")
					err := c.save(next)
					if err == nil && operation == "switch-back" {
						err = c.save(original)
					}
					actionDone <- err
				}
			}()
			select {
			case err := <-actionDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("slow cache reader blocked " + operation)
			}
			unblock()
			result := <-readDone
			if operation != "close" {
				if result.err != nil || result.page.StorageID != "old" {
					t.Fatal(result)
				}
			} else if result.err == nil || len(result.page.Entries) != 0 {
				t.Fatal("stale cache escaped", result)
			}
		})
	}
}

func TestCacheSnapshotUsesCurrentScanProtection(t *testing.T) {
	c := testConsole(t)
	page, err := c.cacheSnapshot(func(string) (cacheproxy.CachePage, error) {
		c.mu.Lock()
		c.batch = Batch{Running: true, ScanOnly: true}
		c.mu.Unlock()
		return cacheproxy.CachePage{Entries: []cacheproxy.CacheEntry{{Key: "item"}}}, nil
	})
	c.mu.Lock()
	c.batch = Batch{}
	c.mu.Unlock()
	if err != nil || !page.Entries[0].Protected {
		t.Fatal(page, err)
	}
	_, err = c.cacheSnapshot(func(string) (cacheproxy.CachePage, error) { return cacheproxy.CachePage{}, context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCacheAPIScanAndStaleStorageProtection(t *testing.T) {
	c := testConsole(t)
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	b, _ := json.Marshal(map[string]any{"storageID": cacheproxy.CacheStorageID(c.settings.StorageDir), "entries": []cacheproxy.CacheSelection{{Key: strings.Repeat("a", 32), Stamp: "old"}}})
	c.batch = Batch{Running: true, ScanOnly: true}
	w := post("/api/cache/delete", string(b))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "扫描校验") {
		t.Fatal(w.Code, w.Body.String())
	}
	c.batch = Batch{}
	for _, path := range []string{"/api/cache/open", "/api/cache/delete"} {
		w = post(path, `{"storageID":"stale","entries":[]}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "存储目录已切换") {
			t.Fatal(w.Code, w.Body.String())
		}
		w = post(path, `{"path":"C:/Windows"}`)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("rejected action started engine")
	}
}
