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
	cfg.ResolvePlayback = func(context.Context, string) (string, error) {
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
func failingQueueWorker(t *testing.T, songs []vrclog.Song) (*queueTask, *atomic.Int32) {
	t.Helper()
	q := &queueTask{}
	q.setQueueSongs(songs, false)
	engine, calls := failingQueueEngine(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.queueWorker(ctx, engine, make(chan struct{}, 1)) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	synctest.Wait()
	return q, calls
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
		q := &queueTask{}
		q.start(tail, engine)
		t.Cleanup(q.stop)
		synctest.Wait()
		logs := "[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":1}]\n" +
			"[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":2}]\n"
		if err := os.WriteFile(path, []byte(logs), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(vrclog.PollInterval)
		synctest.Wait()
		status := q.snapshot()
		latest := len(status.Failures) == 1 && status.Failures[0].ID == 2
		if !latest || calls.Load() != 1 {
			t.Fatal("log batch admitted an intermediate queue", calls.Load())
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		status = q.snapshot()
		cleared := len(status.Songs) == 0 && len(status.Failures) == 0 && status.LogError == "" && status.Running
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

func TestQueueRestartClearsPreviousRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, calls := failingQueueEngine(t)
		tail := &vrclog.Tail{Dir: t.TempDir()}
		if _, err := tail.Poll(time.Now()); err != nil {
			t.Fatal(err)
		}
		q := &queueTask{}
		t.Cleanup(q.stop)
		q.start(tail, engine)
		q.setQueueSongs([]vrclog.Song{{ID: 1}}, false)
		synctest.Wait()
		if status := q.snapshot(); calls.Load() != 1 || len(status.Failures) != 1 {
			t.Fatal("first run did not record its failure", calls.Load(), status)
		}
		q.stop()
		if status := q.snapshot(); status.Running || len(status.Songs) != 0 || len(status.Failures) != 0 {
			t.Fatal("stop left the previous queue visible", status)
		}
		q.start(tail, engine)
		synctest.Wait()
		if status := q.snapshot(); !status.Running || len(status.Songs) != 0 || len(status.Failures) != 0 || calls.Load() != 1 {
			t.Fatal("restart restored the previous queue", calls.Load(), status)
		}
		q.setQueueSongs([]vrclog.Song{{ID: 1}}, false)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("previous retry state blocked the new run", calls.Load())
		}
	})
}

func TestQueueIdleDirectoryWaitsForNewLogs(t *testing.T) {
	for _, existingLog := range []bool{false, true} {
		name := "empty-directory"
		if existingLog {
			name = "old-log"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				engine, calls := failingQueueEngine(t)
				dir := t.TempDir()
				if existingLog {
					if err := os.WriteFile(filepath.Join(dir, "output_log_2020-01-01.txt"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				tail := &vrclog.Tail{Dir: dir}
				if _, err := tail.Poll(time.Now()); err != nil {
					t.Fatal(err)
				}
				q := &queueTask{}
				q.start(tail, engine)
				t.Cleanup(q.stop)
				synctest.Wait()
				time.Sleep(6 * time.Minute)
				synctest.Wait()
				status := q.snapshot()
				healthy := status.Running && status.LogError == "" && status.Error == ""
				if !healthy || calls.Load() != 0 {
					t.Fatal("idle directory was reported as a failure or started work")
				}
				path := filepath.Join(dir, "output_log_2026-10-28.txt")
				logs := "[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":1}]\n"
				if err := os.WriteFile(path, []byte(logs), 0600); err != nil {
					t.Fatal(err)
				}
				time.Sleep(vrclog.DiscoverInterval)
				synctest.Wait()
				status = q.snapshot()
				discovered := status.File == filepath.Base(path) && len(status.Songs) == 1 && status.Songs[0].ID == 1 && status.LogError == ""
				if !discovered || calls.Load() != 1 {
					t.Fatal("new log was not consumed after idle waiting")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				time.Sleep(6 * time.Minute)
				synctest.Wait()
				status = q.snapshot()
				readFailed := status.LogError != "" && len(status.Songs) == 0
				if !readFailed {
					t.Fatal("actual read failure was hidden after the idle timeout")
				}
			})
		})
	}
}

func TestQueueChangesWakeIdleWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, calls := failingQueueWorker(t, nil)
		for _, reset := range []bool{false, true} {
			q.setQueueSongs([]vrclog.Song{{ID: 1}}, reset)
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
		q, calls := failingQueueWorker(t, []vrclog.Song{{ID: 1}})
		if calls.Load() != 1 {
			t.Fatal("initial failure missing", calls.Load())
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("retried before deadline", calls.Load())
		}
		// An unrelated snapshot must not postpone the absolute retry deadline.
		q.setQueueSongs([]vrclog.Song{{ID: 1}}, false)
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("retry deadline did not wake preparation", calls.Load())
		}
		q.setQueueSongs(nil, false)
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
		q, calls := failingQueueWorker(t, []vrclog.Song{{ID: 1}})
		time.Sleep(10 * time.Second)
		q.setQueueSongs([]vrclog.Song{{ID: 1}, {ID: 2}}, false)
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
		q.setQueueSongs([]vrclog.Song{{ID: 2}}, false)
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
