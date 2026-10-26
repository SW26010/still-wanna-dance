package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func failingQueueEngine(t *testing.T) (*cacheproxy.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	cfg := fixtureCacheConfig()
	store, err := cacheproxy.CreateStore(filepath.Join(t.TempDir(), "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg.Store, cfg.Work, cfg.VideoDir = store, testWorkDirectory(t), t.TempDir()
	cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "", errors.New("fixture unavailable")
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	return engine, calls
}

// The worker has no log reader or external wake producer. Fake time exercises
// real retry deadlines against an isolated engine without network requests.
func failingQueueWorker(t *testing.T, songs []vrclog.Song) (*Console, *atomic.Int32) {
	t.Helper()
	c := &Console{queue: QueueStatus{Running: true, Songs: songs}}
	engine, calls := failingQueueEngine(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); c.queueWorker(ctx, engine, make(chan struct{}, 1)) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	synctest.Wait()
	return c, calls
}

func TestQueueLogBatchAndStaleReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, calls := failingQueueEngine(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "output_log_2026-10-24_00-00-00.txt")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		tail := &vrclog.Tail{Dir: dir}
		if _, err := tail.Poll(time.Now()); err != nil {
			t.Fatal(err)
		}
		c := &Console{queue: QueueStatus{Running: true}}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go c.runQueue(ctx, tail, engine, done)
		t.Cleanup(func() { cancel(); <-done })
		synctest.Wait()
		logs := "[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":1}]\n" +
			"[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":2}]\n"
		if err := os.WriteFile(path, []byte(logs), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(vrclog.PollInterval)
		synctest.Wait()
		c.mu.Lock()
		latest := len(c.queue.Failures) == 1 && c.queue.Failures[0].ID == 2
		c.mu.Unlock()
		if !latest || calls.Load() != 1 {
			t.Fatal("log batch admitted an intermediate queue", calls.Load())
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		c.mu.Lock()
		cleared := len(c.queue.Songs) == 0 && len(c.queue.Failures) == 0 && c.queue.LogError != ""
		c.mu.Unlock()
		if !cleared {
			t.Fatal("stale log did not withdraw the queue")
		}
		before := calls.Load()
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != before {
			t.Fatal("stale queue kept retrying", calls.Load())
		}
	})
}

func TestQueueChangesWakeIdleWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, calls := failingQueueWorker(t, nil)
		for _, reset := range []bool{false, true} {
			c.mu.Lock()
			c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, reset)
			c.mu.Unlock()
			synctest.Wait()
			want := int32(1)
			if reset {
				want = 2 // A new room clears the old room's retry delay.
			}
			if calls.Load() != want {
				t.Fatalf("queue change did not wake preparation: calls=%d want=%d", calls.Load(), want)
			}
		}
	})
}

func TestQueueRetryDeadlineWithoutWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, calls := failingQueueWorker(t, []vrclog.Song{{ID: 1}})
		if calls.Load() != 1 {
			t.Fatal("initial failure missing", calls.Load())
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("retried before deadline", calls.Load())
		}
		// An unrelated snapshot must not postpone the absolute retry deadline.
		c.mu.Lock()
		c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false)
		c.mu.Unlock()
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("retry deadline did not wake preparation", calls.Load())
		}
		c.mu.Lock()
		c.setQueueSongsLocked(nil, false)
		c.mu.Unlock()
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("removed song was retried", calls.Load())
		}
	})
}

func TestQueueRetriesEarliestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, calls := failingQueueWorker(t, []vrclog.Song{{ID: 1}})
		time.Sleep(10 * time.Second)
		c.mu.Lock()
		c.setQueueSongsLocked([]vrclog.Song{{ID: 1}, {ID: 2}}, false)
		c.mu.Unlock()
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("second song was not attempted", calls.Load())
		}
		for _, step := range []struct {
			wait time.Duration
			want int32
		}{{49 * time.Second, 2}, {time.Second, 3}, {9 * time.Second, 3}, {time.Second, 4}} {
			time.Sleep(step.wait)
			synctest.Wait()
			if calls.Load() != step.want {
				t.Fatalf("retry at %v: calls=%d want=%d", time.Now(), calls.Load(), step.want)
			}
		}
		// Removing the earliest retry must leave the later song's deadline intact.
		c.mu.Lock()
		c.setQueueSongsLocked([]vrclog.Song{{ID: 2}}, false)
		c.mu.Unlock()
		synctest.Wait()
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if calls.Load() != 4 {
			t.Fatal("removed retry changed the later deadline", calls.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 5 {
			t.Fatal("later retry did not fire", calls.Load())
		}
	})
}
