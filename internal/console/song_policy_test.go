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
	createReaderLibrary(t, c)
	if err := os.Remove(libraryPaths(c.settings).VideoDir); err != nil {
		t.Fatal(err)
	}
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
		body := fmt.Sprintf(`{"storageID":%q,"id":"42","policy":%q}`, cacheproxy.DatabaseID(libraryPaths(c.settings).DatabasePath), policy)
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
	if c.service != nil || c.httpServer != nil || c.batch.Running || c.queue.Running {
		t.Fatal("marking started work")
	}
	if _, err := os.Stat(libraryPaths(c.settings).VideoDir); !os.IsNotExist(err) {
		t.Fatal("preference operations recreated the missing video directory", err)
	}
}

func TestOfflineSongFavoritePrecedesStartupRetention(t *testing.T) {
	c := testConsole(t) // no engine; the active storage budget is zero
	cfg := fixtureCacheConfig()
	cfg.CacheUnlimited = true
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	const body = "previously saved ordinary video"
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	if err := engine.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261021000000", Source: "test", Songs: []cacheproxy.CatalogSong{{ID: 42, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cfg.VideoDir, key+".mp4")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(cfg.VideoDir, ".swd-tmp", "download-block-1234.part")
	if err := os.WriteFile(partial, []byte("previous run"), 0600); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"storageID":%q,"id":"42","policy":"favorite"}`, cacheproxy.DatabaseID(fixtureLibraryPaths(filepath.Dir(cfg.VideoDir)).DatabasePath))
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/songs", strings.NewReader(input))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if saved, err := os.ReadFile(file); err != nil || string(saved) != body {
		t.Fatal("marking favorite deleted the existing video", err)
	}
	if c.service != nil {
		t.Fatal("marking favorite started an engine")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal("marking favorite ran startup cleanup", err)
	}
	page, err := cacheproxy.ReadSongPage(t.Context(), fixtureLibraryPaths(filepath.Dir(cfg.VideoDir)), "42", cacheproxy.SongFavorite, 0)
	if err != nil || page.Total != 1 || !page.Entries[0].Saved {
		t.Fatal(page, err)
	}
	// Ordinary engine startup must now see the saved preference before eviction.
	c.lifecycleMu.Lock()
	_, err = c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := os.ReadFile(file); err != nil || string(saved) != body {
		t.Fatal("startup ignored the offline favorite", err)
	}
}

func TestOfflineSongPolicyRejectsMissingUsedLibrary(t *testing.T) {
	c := testConsole(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(testStorageRoot(c.settings), testStorageRoot(c.settings)+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	c.inventory.CoverageKnown = true
	input := fmt.Sprintf(`{"storageID":%q,"id":"42","policy":"favorite"}`, cacheproxy.DatabaseID(libraryPaths(c.settings).DatabasePath))
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/songs", strings.NewReader(input))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(testStorageRoot(c.settings)); !os.IsNotExist(err) {
		t.Fatal("missing used library was recreated", err)
	}
}

func TestOfflineSongPolicyDoesNotRequireVideoDirectory(t *testing.T) {
	c := testConsole(t)
	createReaderLibrary(t, c)
	paths := libraryPaths(c.settings)
	if err := os.Remove(paths.VideoDir); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"storageID":%q,"id":"42","policy":"favorite"}`, cacheproxy.DatabaseID(paths.DatabasePath))
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/songs", strings.NewReader(input))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	page, err := cacheproxy.ReadSongPage(t.Context(), paths, "42", cacheproxy.SongFavorite, 0)
	if err != nil || page.Total != 1 || page.VideoError == "" {
		t.Fatal(page, err)
	}
	if c.service != nil {
		t.Fatal("preference change started engine")
	}
	if _, err := os.Stat(paths.VideoDir); !os.IsNotExist(err) {
		t.Fatal("preference change recreated video directory", err)
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
	wait(func() bool { return c.queue.Completed == 1 })
	if calls.Load() != 1 {
		t.Fatal("unblocked preparation was not admitted once", calls.Load())
	}
}

func TestBatchContinuesFavoritesAfterBudgetStopAndSkipsBlockedSharedSong(t *testing.T) {
	for _, retain := range []bool{true, false} {
		t.Run(fmt.Sprint(retain), func(t *testing.T) { testBatchFavoriteRetention(t, retain) })
	}
}

func testBatchFavoriteRetention(t *testing.T, retain bool) {
	c := testConsole(t)
	c.settings.RetainFavorites = retain
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
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.CacheUnlimited = false
	cfg.RetainFavorites = retain
	cfg.VideoBudgetBytes = 0
	cfg.AggressiveBlockReclamation = "off"
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
	wantDownloads := 0
	if retain {
		wantDownloads = 1
	}
	if int(downloads.Load()) != wantDownloads || queries[9000003] != 1 || queries[9000002] != wantDownloads || queries[9000001] != 0 || c.batch.Failed != 0 || c.batch.Skipped != 1 || c.batch.Downloaded != wantDownloads || !c.batch.BudgetReached {
		t.Fatal(queries, downloads.Load(), c.batch)
	}
	path := filepath.Join(cfg.VideoDir, fmt.Sprintf("%x.mp4", md5.Sum([]byte("favorite"))))
	if !retain {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("disabled retention published favorite beyond budget", err)
		}
		return
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "favorite" {
		t.Fatal("favorite not published", err)
	}
}
