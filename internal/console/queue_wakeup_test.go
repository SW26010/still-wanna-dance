package console

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/vrclog"
)

func TestQueuePersistentLogFailureUsesFaultCenter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, _ := failingQueueEngine(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "output_log_2026-10-24_00-00-00.txt")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		tail := &vrclog.Tail{Dir: dir}
		if _, err := tail.Poll(time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		center := faults.New()
		q := &queueTask{}
		q.setLogFaultObserver(func(err error) { center.Retry("queue:log", err) })
		q.start(tail, engine, vrclog.RoomSnapshot{})
		t.Cleanup(q.stop)
		synctest.Wait()
		time.Sleep(vrclog.DiscoverInterval + vrclog.PollInterval)
		synctest.Wait()
		select {
		case <-center.Wake():
			t.Fatal("a retrying log failure was reported too early")
		default:
		}
		time.Sleep(vrclog.PollInterval)
		synctest.Wait()
		select {
		case <-center.Wake():
			if open, err := center.Take(); !open || err == nil {
				t.Fatal("persistent log failure did not request WebUI", open, err)
			}
		default:
			t.Fatal("persistent log failure was not reported")
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(vrclog.PollInterval)
		synctest.Wait()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		time.Sleep(vrclog.DiscoverInterval + 2*vrclog.PollInterval)
		synctest.Wait()
		select {
		case <-center.Wake():
			if open, err := center.Take(); !open || err == nil {
				t.Fatal("new log incident after recovery was not reported", open, err)
			}
		default:
			t.Fatal("new log incident did not wake the fault center")
		}
	})
}

func failingQueueEngine(t *testing.T) (*cacheproxy.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	cfg := fixtureCacheConfig()
	cfg.ResolvePlayback = func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", errors.New("fixture unavailable")
	}
	return newQueueTestEngine(t, cfg), calls
}

func newQueueTestEngine(t *testing.T, cfg cacheproxy.Config) *cacheproxy.Server {
	t.Helper()
	store, err := cacheproxy.CreateStore(filepath.Join(t.TempDir(), "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg.Store, cfg.Work, cfg.VideoDir = store, testWorkDirectory(t), t.TempDir()
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	return engine
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
		q.start(tail, engine, vrclog.RoomSnapshot{})
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

func TestStartQueueRestoresOnlyFinalSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, calls := failingQueueEngine(t)
		dir := t.TempDir()
		c := &Console{service: engine, settings: Settings{VRChatLogDir: dir}}
		stamp := time.Now().Add(-20 * time.Second).Format("2006.01.02 15:04:05")
		log := stamp + " [Behaviour] Successfully joined room\n" +
			stamp + " [VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = [{\"songId\":1}]\n" +
			stamp + " [VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = [{\"songId\":2}]\n"
		if err := os.WriteFile(filepath.Join(dir, "output_log_test.txt"), []byte(log), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.startQueueLocked(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.queue.stop)
		synctest.Wait()
		if err := c.startQueueLocked(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		status := c.queue.snapshot()
		if len(status.Failures) != 1 || status.Failures[0].ID != 2 || calls.Load() != 1 {
			t.Fatal("enabled queue resolved obsolete work or replayed twice", status, calls.Load())
		}
	})
}

func TestRoomLogAppliesPlaybackAndQueueAndClearsBothOnStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, _ := failingQueueEngine(t)
		end := 100.0
		const md5 = "00000000000000000000000000000001"
		if err := engine.Store.SyncCatalog(t.Context(), cacheproxy.Catalog{
			Revision: "20261119000000", Source: cacheproxy.UdonCatalogURL,
			Songs: []cacheproxy.CatalogSong{{ID: 1, MD5: md5, End: &end}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := engine.Store.ObserveSongURL(t.Context(), cacheproxy.PlaybackObservation{
			SongID: 1, Node: "cf", URL: "http://play.udon.dance/files/1/1-video.mp4?e=" + md5 + "&s=1234",
		}); err != nil {
			t.Fatal(err)
		}
		sub, err := engine.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Close)
		playbackEnd := func() int64 {
			t.Helper()
			synctest.Wait()
			// Block observation batches notifications before publishing a snapshot.
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
			view, _, closed := sub.Snapshot()
			if closed || view == nil {
				t.Fatal("missing engine snapshot")
			}
			for _, demand := range view.Demands {
				if demand.Playback != nil {
					return demand.Playback.EndAt
				}
			}
			return 0
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "output_log_2026-11-19.txt")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		q := &queueTask{}
		t.Cleanup(q.stop)
		start := func() {
			t.Helper()
			tail, room, err := vrclog.OpenRoomLog(dir, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			q.start(tail, engine, room)
			synctest.Wait()
		}
		start()
		at := time.Now().In(time.Local).Truncate(time.Second)
		stamp := at.Format("2006.01.02 15:04:05")
		lines := stamp + " Debug - [Behaviour] Successfully joined room\n" +
			stamp + " Debug - [USharpVideo (WannaDance)] Started video: http://api.udon.dance/Api/Songs/play?id=1, since I'm the owner\n" +
			stamp + " Debug - [VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":42}]\n" +
			stamp + " Debug - [USharpVideo (WannaDance)] Syncing video to 15.00, current time = 0\n"
		if err := os.WriteFile(path, []byte(lines), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(vrclog.PollInterval)
		want := at.Add(85 * time.Second).UnixMilli()
		got := playbackEnd()
		status := q.snapshot()
		if got != want || len(status.Songs) != 1 || status.Songs[0].ID != 42 {
			t.Fatal("one log batch did not deliver playback and queue state", got, want, status)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("[VideoQueueManager] OnPreSerialization: queue info serialized: broken\n")
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(vrclog.PollInterval)
		if got := playbackEnd(); got != want || len(q.snapshot().Songs) != 0 {
			t.Fatal("malformed queue update did not clear only the queue", got, q.snapshot())
		}
		q.stop()
		if got := playbackEnd(); got != 0 || q.snapshot().Running {
			t.Fatal("stopping the reader retained playback or queue state", got, q.snapshot())
		}
		start()
		time.Sleep(vrclog.PollInterval)
		if got := playbackEnd(); got != want || len(q.snapshot().Songs) != 0 {
			t.Fatal("restart lost original playback end or restored malformed queue", got, want, q.snapshot())
		}
		q.stop()
		time.Sleep(90 * time.Second)
		start()
		if got := playbackEnd(); got != 0 || len(q.snapshot().Songs) != 0 {
			t.Fatal("restart installed already expired playback", got, q.snapshot())
		}
	})
}

func TestRoomLogLeaveFreezesQueueHeadBeforeClearingPlayback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const body = "queue handoff fixture"
		const playingKey = "00000000000000000000000000000001"
		queueKey := fmt.Sprintf("%x", md5.Sum([]byte(body)))
		cfg := fixtureCacheConfig()
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			return fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%s&s=%d", queueKey, len(body)), nil
		}
		cfg.ResourceTransports = func(string) []http.RoundTripper {
			return []http.RoundTripper{catalogTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				serveTestVideo(w, r, body)
				return w.Result(), nil
			})}
		}
		engine := newQueueTestEngine(t, cfg)
		end := 300.0
		if err := engine.Store.SyncCatalog(t.Context(), cacheproxy.Catalog{
			Revision: "20261119000000", Source: cacheproxy.UdonCatalogURL,
			Songs: []cacheproxy.CatalogSong{{ID: 1, MD5: playingKey, End: &end}, {ID: 42, MD5: queueKey}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := engine.Store.ObserveSongURL(t.Context(), cacheproxy.PlaybackObservation{
			SongID: 1, Node: "cf", URL: "http://play.udon.dance/files/1/1-video.mp4?e=" + playingKey + "&s=1234",
		}); err != nil {
			t.Fatal(err)
		}
		sub, err := engine.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Close)
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "output_log_2026-11-19.txt"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		tail := &vrclog.Tail{Dir: dir}
		if _, err := tail.Poll(time.Now()); err != nil {
			t.Fatal(err)
		}
		q := &queueTask{}
		q.start(tail, engine, vrclog.RoomSnapshot{})
		t.Cleanup(q.stop)
		synctest.Wait()
		appendLog := func(lines string) *cacheproxy.BlockSnapshot {
			t.Helper()
			if _, err := f.WriteString(lines); err != nil {
				t.Fatal(err)
			}
			time.Sleep(vrclog.PollInterval)
			synctest.Wait()
			time.Sleep(100 * time.Millisecond) // Allow the observer to publish the batch.
			synctest.Wait()
			view, _, closed := sub.Snapshot()
			if view == nil || closed {
				t.Fatal("missing block snapshot")
			}
			return view
		}
		at := time.Now().In(time.Local).Truncate(time.Second)
		want := at.Add(5*time.Minute + 10*time.Second).UnixMilli()
		before := appendLog(at.Format("2006.01.02 15:04:05") + " Debug - [USharpVideo (WannaDance)] Started video: http://api.udon.dance/Api/Songs/play?id=1, since I'm the owner\n" +
			"[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":42}]\n")
		id := queueKey + "/0"
		if detail, ok := before.Detail(id); !ok || len(detail.Sources) != 1 || detail.Sources[0].Kind != "queue" || detail.Sources[0].NeedBy != want {
			t.Fatalf("queue head did not acquire the room clock: %+v", detail)
		}
		after := appendLog("[Behaviour] OnLeftRoom\n")
		detail, ok := after.Detail(id)
		if !ok || len(detail.Sources) != 1 || detail.Sources[0].Kind != "handoff" {
			t.Fatalf("leaving the room did not hand off the queue head: %+v", detail)
		}
		handoff := detail.Sources[0]
		status := q.snapshot()
		if handoff.NeedBy != want || handoff.ExpiresAt != status.Updated.Add(2*time.Second).UnixMilli() {
			t.Fatalf("handoff NeedBy=%d, want %d, expires=%d, left=%d", handoff.NeedBy, want, handoff.ExpiresAt, status.Updated.UnixMilli())
		}
		for _, demand := range after.Demands {
			if demand.Playback != nil {
				t.Fatal("leaving the room retained its playback clock")
			}
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
		q.start(tail, engine, vrclog.RoomSnapshot{})
		q.setQueueSongs([]vrclog.Song{{ID: 1}}, false)
		synctest.Wait()
		if status := q.snapshot(); calls.Load() != 1 || len(status.Failures) != 1 {
			t.Fatal("first run did not record its failure", calls.Load(), status)
		}
		q.stop()
		if status := q.snapshot(); status.Running || len(status.Songs) != 0 || len(status.Failures) != 0 {
			t.Fatal("stop left the previous queue visible", status)
		}
		q.start(tail, engine, vrclog.RoomSnapshot{})
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
				q.start(tail, engine, vrclog.RoomSnapshot{})
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
				q.mu.Lock()
				generation := q.generation
				q.mu.Unlock()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				time.Sleep(vrclog.PollInterval)
				synctest.Wait()
				if status := q.snapshot(); status.LogError != "" || status.File != "" || len(status.Songs) != 0 {
					t.Fatalf("watcher did not withdraw the deleted log: %+v", status)
				}
				time.Sleep(6 * time.Minute)
				synctest.Wait()
				status = q.snapshot()
				if !status.Running || status.LogError != "" || status.File != "" || len(status.Songs) != 0 {
					t.Fatalf("watcher did not return to healthy idle state: %+v", status)
				}
				q.mu.Lock()
				resets := q.generation - generation
				q.mu.Unlock()
				if resets != 1 {
					t.Fatalf("the missing log reset the queue %d times, want one", resets)
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
