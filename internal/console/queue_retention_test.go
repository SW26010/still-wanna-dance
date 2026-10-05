package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
		if r.URL.Path == "/Api/Songs/list" {
			io.WriteString(w, `{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":1,"name":"one"}]}]}}`)
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
	c.client.Transport = http.DefaultTransport
	cfg := cacheproxy.DefaultConfig()
	cfg.OriginScheme = "http"
	cfg.StorageDir = c.settings.StorageDir
	cfg.MaxCacheBytes = limit
	cfg.Origins["nya.xin.moe"] = strings.TrimPrefix(origin.URL, "http://")
	cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	return c, engine, body, calls
}

func TestQueueMoveOutsidePrefetchWindowDuringDownload(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var downloads atomic.Int32
	c, engine, body, _ := queueRetentionFixture(t, 1, func() {
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
	wait(func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.queue.waiters) == 0
	})
	release.Do(func() { close(finish) })
	path := fixtureVideoPath(c.settings.StorageDir, "1", body)
	wait(func() bool { _, err := os.Stat(path); return err == nil })
	// Let the asynchronous retention worker handle publication and release.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("queued download was evicted", err)
	}
	c.mu.Lock()
	c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	wait(func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.queue.Completed == 1
	})
	if downloads.Load() != 1 {
		t.Fatal("reentry redownloaded queued cache")
	}
	c.mu.Lock()
	c.setQueueSongsLocked(nil, true)
	c.mu.Unlock()
	wait(func() bool { _, err := os.Stat(path); return os.IsNotExist(err) })
}

func TestQueueReentryAfterEviction(t *testing.T) {
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
	// A room reset releases the old cache immediately, allowing eviction.
	c.setQueueSongsLocked([]vrclog.Song{{ID: 2}}, true)
	c.mu.Unlock()
	wake <- struct{}{}
	wait(2)
	path := fixtureVideoPath(c.settings.StorageDir, "1", body)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("fixture did not evict song 1", err)
	}
	before := calls.Load()
	c.mu.Lock()
	c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	wait(3)
	t.Logf("song 1 evicted then requeued: API calls before=%d after=%d", before, calls.Load())
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
	if !strings.Contains(c.batch.Phase, "设置") {
		t.Fatal("budget stop must direct users to settings")
	}
}

func TestQueueProtectsOversizedCacheOutsidePrefetchWindow(t *testing.T) {
	c, engine, body, calls := queueRetentionFixture(t, 1)
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
	c.setQueueSongsLocked([]vrclog.Song{{ID: -1}, {ID: -1}, {ID: -1}, {ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	path := fixtureVideoPath(c.settings.StorageDir, "1", body)
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("queued cache outside prefetch window was evicted", err)
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
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("removed song remained protected")
}

func TestQueueCoalescedDepartureInvalidatesPreparedSong(t *testing.T) {
	q := QueueStatus{Songs: []vrclog.Song{{ID: 1}}, prepared: map[int64]bool{1: true}}
	q.setSongs([]vrclog.Song{{ID: 2}, {ID: 3}, {ID: 4}, {ID: 1}}, false)
	q.setSongs([]vrclog.Song{{ID: 1}}, false)
	if q.prepared[1] {
		t.Fatal("song moved outside first three then returned before worker wake")
	}
}
