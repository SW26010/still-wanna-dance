package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
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
	"still-wanna-dance/internal/vrclog"
)

func queueRetentionFixture(t *testing.T, limit int64, beforeDownload ...func()) (*Console, *cacheproxy.Server, string, *atomic.Int32) {
	t.Helper()
	c := testConsole(t)
	body := "audit video content"
	calls := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, before := range beforeDownload {
			before()
		}
		data := body
		if !strings.Contains(r.URL.Path, "/1-") {
			data = "other video content"
		}
		io.WriteString(w, data)
	}))
	t.Cleanup(origin.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog" {
			writeTestMD5Catalog(w, map[int]string{1: body})
			return
		}
		calls.Add(1)
		host := "nya.xin.moe"
		if r.URL.Query().Get("node") == "cf" {
			host = "play.udon.dance"
		}
		data := body
		if r.URL.Query().Get("id") != "1" {
			data = "other video content"
		}
		w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/%s-abc.mp4?e=%x&s=%d", host, r.URL.Query().Get("id"), md5.Sum([]byte(data)), len(data)))
		w.WriteHeader(302)
	}))
	t.Cleanup(api.Close)
	c.apiBase = api.URL
	c.checksumURL = api.URL + "/catalog"
	c.client.Transport = http.DefaultTransport
	cfg := fixtureCacheConfig()
	cfg.Work = c.work
	cfg.ResolvePlayback = c.resolvePlayback
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.VideoBudgetBytes = limit
	cfg.CacheUnlimited = false
	cfg.Origins["nya.xin.moe"] = strings.TrimPrefix(origin.URL, "http://")
	cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	return c, engine, body, calls
}

func TestQueueReorderKeepsInFlightPreparation(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var downloads atomic.Int32
	c, engine, body, _ := queueRetentionFixture(t, 0, func() {
		if downloads.Add(1) == 1 {
			close(started)
			<-finish
		}
	})
	var release sync.Once
	defer release.Do(func() { close(finish) })
	c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	done, wake := make(chan struct{}), make(chan struct{}, 1)
	go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
	defer func() { cancel(); <-done }()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("queue transition timed out")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	c.mu.Lock()
	c.setQueueSongsLocked([]vrclog.Song{{ID: -1}, {ID: -1}, {ID: -1}, {ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	c.mu.Lock()
	waiter := c.queue.waiters[1]
	c.mu.Unlock()
	if waiter == nil || waiter.ctx.Err() != nil {
		t.Fatal("reordering within the queue canceled preparation")
	}
	// Restore its time window before completing the body. A far-future queue
	// position alone does not protect bytes under a zero retention budget.
	c.mu.Lock()
	c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	release.Do(func() { close(finish) })
	var path string
	wait(func() bool {
		files, err := filepath.Glob(filepath.Join(c.work.Path(), "download-block-*.part"))
		if err != nil || len(files) != 1 {
			return false
		}
		path = files[0]
		return true
	})
	if _, err := os.Stat(fixtureVideoPath(testStorageRoot(c.settings), "1", body)); !os.IsNotExist(err) {
		t.Fatal("zero budget published a video", err)
	}
	// Let the asynchronous retention worker handle publication and release.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("queued download was evicted", err)
	}
	wait(func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.queue.Completed == 1
	})
	if downloads.Load() != 1 {
		t.Fatal("reordering redownloaded queued cache")
	}
	c.mu.Lock()
	c.setQueueSongsLocked(nil, true)
	c.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("reset lost fixed local protection", err)
	}
}

func TestQueueReentryDuringProtectionAfterRoomReset(t *testing.T) {
	c, engine, body, calls := queueRetentionFixture(t, int64(len("audit video content")))
	c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	wake := make(chan struct{}, 1)
	go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
	defer func() { cancel(); <-done }()
	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			c.mu.Lock()
			ok := c.queue.Completed >= n
			c.mu.Unlock()
			if ok {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("queue timeout")
	}
	wait(1)
	c.mu.Lock()
	// A room reset ends the old source; the fixed local grace still applies.
	c.setQueueSongsLocked([]vrclog.Song{{ID: 2}}, true)
	c.mu.Unlock()
	wake <- struct{}{}
	wait(2)
	path := fixtureVideoPath(testStorageRoot(c.settings), "1", body)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("reset lost protected song", err)
	}
	before := calls.Load()
	c.mu.Lock()
	c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	wait(3)
	t.Logf("song 1 requeued during protection: API calls before=%d after=%d", before, calls.Load())
	if _, err := os.Stat(path); err != nil {
		t.Fatal("requeued song was not restored", err)
	}
}

func TestBatchReportsBudgetStopWithoutDownloading(t *testing.T) {
	var downloads atomic.Int32
	c, _, _, calls := queueRetentionFixture(t, 1, func() { downloads.Add(1) })
	defer c.Close()
	if err := c.startBatch(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	<-done
	inventory := scanInventory(context.Background(), c.settings)
	t.Logf("phase=%s downloaded=%d actualVideos=%d", c.batch.Phase, c.batch.Downloaded, inventory.Videos)
	if inventory.Error != "" || inventory.Videos != 0 || downloads.Load() != 0 || calls.Load() != 1 || !c.lastBatch.Updated.IsZero() || !c.batch.BudgetReached || c.batch.Failed != 0 {
		t.Fatalf("unexpected inventory or snapshot: %+v %+v", inventory, c.lastBatch)
	}
	if !strings.Contains(c.batch.Phase, "调整视频保留空间") {
		t.Fatal("budget stop must direct users to settings")
	}
}

func TestQueueDepartureKeepsTemporaryBytesAvailable(t *testing.T) {
	c, engine, body, calls := queueRetentionFixture(t, 0)
	c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	done, wake := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		ready := c.queue.Completed == 1
		c.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		c.mu.Lock()
		c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false)
		c.mu.Unlock()
		select {
		case wake <- struct{}{}:
		case <-time.After(time.Second):
			t.Fatal("worker stalled")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unchanged queue redownloaded video", calls.Load())
	}
	c.mu.Lock()
	c.setQueueSongsLocked(nil, false)
	c.mu.Unlock()
	wake <- struct{}{}
	files, err := filepath.Glob(filepath.Join(c.work.Path(), "download-block-*.part"))
	if err != nil || len(files) != 1 {
		t.Fatal("queue did not keep a complete temporary block", files, err)
	}
	path := files[0]
	if _, err := os.Stat(fixtureVideoPath(testStorageRoot(c.settings), "1", body)); !os.IsNotExist(err) {
		t.Fatal("zero budget published a video", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("departed queue head lost handoff protection", err)
	}
	c.mu.Lock()
	c.setQueueSongsLocked(nil, false)
	c.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("ordinary empty queue lost handoff", err)
	}
	c.mu.Lock()
	c.setQueueSongsLocked(nil, true)
	c.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("reset removed existing protection tail", err)
	}
}

func TestQueueCoalescedDepartureInvalidatesPreparedSong(t *testing.T) {
	q := QueueStatus{Songs: []vrclog.Song{{ID: 1}}, prepared: map[int64]bool{1: true}}
	q.setSongs([]vrclog.Song{{ID: 2}, {ID: 3}, {ID: 4}, {ID: 1}}, false)
	if !q.prepared[1] {
		t.Fatal("reordering a queued song discarded preparation")
	}
	q.setSongs([]vrclog.Song{{ID: 2}, {ID: 3}, {ID: 4}}, false)
	q.setSongs([]vrclog.Song{{ID: 1}}, false)
	if q.prepared[1] {
		t.Fatal("song left the queue then returned before worker wake")
	}
}
