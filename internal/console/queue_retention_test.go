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
	"sync/atomic"
	"testing"
	"time"

	"stepstash/internal/cacheproxy"
	"stepstash/internal/vrclog"
)

func queueRetentionFixture(t *testing.T, limit int64) (*Console, *cacheproxy.Server, string, *atomic.Int32) {
	t.Helper()
	c := testConsole(t)
	body := "audit video content"
	calls := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	t.Cleanup(origin.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Api/Songs/list" {
			io.WriteString(w, `{"groups":{"contents":[{"songInfos":[{"id":1,"name":"one"}]}]}}`)
			return
		}
		calls.Add(1)
		w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/%s-abc.mp4?e=%x&s=%d", r.URL.Query().Get("id"), md5.Sum([]byte(body)), len(body)))
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
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	return c, engine, body, calls
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
	c.queue.setSongs([]vrclog.Song{{ID: 2}}, false)
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
	c.queue.setSongs([]vrclog.Song{{ID: 1}}, false)
	c.mu.Unlock()
	wake <- struct{}{}
	wait(3)
	t.Logf("song 1 evicted then requeued: API calls before=%d after=%d", before, calls.Load())
	if _, err := os.Stat(path); err != nil {
		t.Fatal("requeued song was not restored", err)
	}
}

func TestBatchReportsCompletionWithEviction(t *testing.T) {
	c, _, _, _ := queueRetentionFixture(t, 1)
	if err := c.startBatch(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	<-done
	inventory := scanInventory(c.settings)
	t.Logf("phase=%s downloaded=%d actualVideos=%d", c.batch.Phase, c.batch.Downloaded, inventory.Videos)
	if inventory.Error != "" || inventory.Videos != 0 || c.lastBatch.Downloaded != 1 || c.lastBatch.Updated.IsZero() {
		t.Fatalf("unexpected inventory or snapshot: %+v %+v", inventory, c.lastBatch)
	}
	if !strings.Contains(c.lastBatch.Phase, "淘汰") {
		t.Fatal("completion must explain that downloaded files may not be retained")
	}
}

func TestQueueImmediateEvictionDoesNotRedownloadUnchangedQueue(t *testing.T) {
	c, engine, _, calls := queueRetentionFixture(t, 1)
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
		c.queue.setSongs([]vrclog.Song{{ID: 1}}, false)
		c.mu.Unlock()
		select {
		case wake <- struct{}{}:
		case <-time.After(time.Second):
			t.Fatal("worker stalled")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unchanged queue redownloaded evicted video", calls.Load())
	}
}

func TestQueueCoalescedDepartureInvalidatesPreparedSong(t *testing.T) {
	q := QueueStatus{Songs: []vrclog.Song{{ID: 1}}, prepared: map[int64]bool{1: true}}
	q.setSongs([]vrclog.Song{{ID: 2}, {ID: 3}, {ID: 4}, {ID: 1}}, false)
	q.setSongs([]vrclog.Song{{ID: 1}}, false)
	if q.prepared[1] {
		t.Fatal("song moved outside first three then returned before worker wake")
	}
}
