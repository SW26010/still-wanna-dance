package console

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func TestSongPolicyAPIReadAuthValidationAndPersistence(t *testing.T) {
	c := testConsole(t)
	call := func(method, path, body, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-SWD-Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/songs", "", c.token, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if c.service != nil {
		t.Fatal("reading preferences started engine")
	}
	for _, method := range []string{"GET", "POST"} {
		for _, credentials := range [][2]string{{"", ""}, {c.token, "http://evil.example"}} {
			if w := call(method, "/api/songs", "{}", credentials[0], credentials[1]); w.Code != 403 {
				t.Fatal(w.Code)
			}
		}
	}
	for _, path := range []string{"/api/songs?offset=-1", "/api/songs?policy=unknown", "/api/songs?q=" + strings.Repeat("x", 301)} {
		if w := call("GET", path, "", c.token, ""); w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	for _, body := range []string{`{"storageID":"stale","id":"1","policy":"favorite"}`, `{"id":"0","policy":"favorite"}`, `{"id":"1","policy":"unknown"}`} {
		if w := call("POST", "/api/songs", body, c.token, ""); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, policy := range []string{"favorite", "blocked", "normal"} {
		body := fmt.Sprintf(`{"storageID":%q,"id":"42","policy":%q}`, cacheproxy.CacheStorageID(c.settings.StorageDir), policy)
		if w := call("POST", "/api/songs", body, c.token, ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		w := call("GET", "/api/songs?q=42", "", c.token, "")
		var page cacheproxy.SongPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != 1 || page.Entries[0].Policy != cacheproxy.SongPolicy(policy) || page.Entries[0].Saved {
			t.Fatal(page)
		}
	}
	if c.httpServer != nil || c.batch.Running || c.queue.Running {
		t.Fatal("marking started work")
	}
}

func TestQueueSkipsBlockedWithoutRetryAndResumesAfterUnblock(t *testing.T) {
	c, engine, _, calls := queueRetentionFixture(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	if err := engine.SetSongPolicy(ctx, 1, cacheproxy.SongBlocked); err != nil {
		t.Fatal(err)
	}
	if _, err := c.prefetchSong(ctx, engine, 1); err != cacheproxy.ErrSongBlocked {
		t.Fatal(err)
	}
	c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}, {ID: -1}}}
	done, wake := make(chan struct{}), make(chan struct{}, 1)
	go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
	defer func() { cancel(); <-done }()
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			c.mu.Lock()
			ok := predicate()
			c.mu.Unlock()
			if ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("queue transition timeout")
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(func() bool { return slices.Contains(c.queue.Blocked, 1) })
	for range 3 {
		wake <- struct{}{}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	clean := c.queue.Completed == 0 && len(c.queue.Failures) == 0 && len(c.queue.Songs) == 2
	c.mu.Unlock()
	if !clean || calls.Load() != 0 {
		t.Fatal("blocked song retried or disappeared", clean, calls.Load())
	}
	if err := engine.SetSongPolicy(ctx, 1, cacheproxy.SongNormal); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	wait(func() bool { return c.queue.Completed == 1 })
	if calls.Load() != 1 {
		t.Fatal("unblocked preparation was not admitted once", calls.Load())
	}
}

func TestBatchContinuesFavoritesAfterBudgetStopAndSkipsBlockedSharedSong(t *testing.T) {
	c := testConsole(t)
	// IDs outside the initial priority list keep deterministic descending order:
	// ordinary hits the zero budget first; favorite has a blocked shared ID.
	bodies := map[int]string{9000003: "ordinary", 9000002: "favorite", 9000001: "favorite"}
	var mu sync.Mutex
	queries := map[int]int{}
	var downloads atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); fmt.Fprint(w, "favorite") }))
	defer origin.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog" {
			writeTestMD5Catalog(w, bodies)
			return
		}
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		mu.Lock()
		queries[id]++
		mu.Unlock()
		body := bodies[id]
		w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/2403/%d-abc.mp4?e=%x&s=%d", id, md5.Sum([]byte(body)), len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/catalog", http.DefaultTransport
	cfg := fixtureCacheConfig()
	cfg.StorageDir = c.settings.StorageDir
	cfg.OriginScheme = "http"
	cfg.CacheUnlimited = false
	cfg.MaxCacheBytes = 0
	cfg.AggressiveCacheReclamation = "off"
	cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	if err := engine.SetSongPolicy(t.Context(), 9000002, cacheproxy.SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := engine.SetSongPolicy(t.Context(), 9000001, cacheproxy.SongBlocked); err != nil {
		t.Fatal(err)
	}
	if err := c.startBatch(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("batch stalled")
	}
	mu.Lock()
	defer mu.Unlock()
	if downloads.Load() != 1 || queries[9000003] != 1 || queries[9000002] != 1 || queries[9000001] != 0 || c.batch.Failed != 0 || c.batch.Skipped != 1 || c.batch.Downloaded != 1 || !c.batch.BudgetReached {
		t.Fatal(queries, downloads.Load(), c.batch)
	}
	path := filepath.Join(cfg.StorageDir, "videos", fmt.Sprintf("%x.mp4", md5.Sum([]byte("favorite"))))
	if data, err := os.ReadFile(path); err != nil || string(data) != "favorite" {
		t.Fatal("favorite not published", err)
	}
}
