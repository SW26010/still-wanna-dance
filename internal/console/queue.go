package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

type QueueStatus struct {
	Running       bool          `json:"running"`
	File          string        `json:"file"`
	Songs         []vrclog.Song `json:"songs"`
	Current       int64         `json:"current"`
	Active        []int64       `json:"active"`
	Completed     int           `json:"completed"`
	Error         string        `json:"error"`
	Failures      []Failure     `json:"failures"`
	LogError      string        `json:"logError"`
	Updated       time.Time     `json:"updated"`
	Generation    uint64        `json:"-"`
	waiters       map[int64]*queueWaiter
	prepared      map[int64]bool
	protect       func([]int64, bool)
	prefetchCount int
}

func (q *QueueStatus) windowSize() int {
	if q.prefetchCount == 0 {
		return 3
	}
	return q.prefetchCount
}

type queueWaiter struct {
	ctx        context.Context
	cancel     context.CancelFunc
	generation uint64
}

func (q *QueueStatus) wants(id int64, generation uint64) bool {
	if generation != q.Generation {
		return false
	}
	for i, song := range q.Songs {
		if i >= q.windowSize() {
			break
		}
		if song.ID == id {
			return true
		}
	}
	return false
}

// Call with c.mu held. Reset errors even when the next room has the same IDs.
func (q *QueueStatus) setSongs(songs []vrclog.Song, reset bool) {
	q.Songs = songs
	if reset {
		q.Generation++
		q.Failures = nil
		q.prepared = nil
	}
	// Remember success only while a song stays in the prefetch window. Do
	// this on every snapshot, including snapshots coalesced before a wake.
	// Reentry checks the current version again; cached songs remain protected
	// throughout the full queue, including outside the prefetch window.
	for id := range q.prepared {
		if !q.wants(id, q.Generation) {
			delete(q.prepared, id)
		}
	}
	// Cancel the caller synchronously with invalidation, before a newly freed
	// engine slot can admit an obsolete waiter. Existing flights own their context.
	for id, waiter := range q.waiters {
		if !q.wants(id, waiter.generation) {
			if waiter.ctx.Err() == nil {
				slog.Info("queue_song_canceled", "trace_id", applog.TraceID(waiter.ctx), "song_id", id, "generation", waiter.generation, "reason", "queue_changed")
			}
			waiter.cancel()
		}
	}
	q.refreshErrors()
}

func queueSongIDs(songs []vrclog.Song) []int64 {
	ids := make([]int64, 0, len(songs))
	for _, song := range songs {
		if song.ID > 0 {
			ids = append(ids, song.ID)
		}
	}
	return ids
}

// The caller holds mu on entry and return. Engine protection may wait on a
// deletion, so release mu before acquiring the independent update lock. This
// lock serializes state snapshots, protection, initialization and teardown;
// a later reset cannot overtake an earlier update and then be overwritten.
func (c *Console) setQueueSongsLocked(songs []vrclog.Song, reset bool) {
	c.mu.Unlock()
	c.queueUpdateMu.Lock()
	c.mu.Lock()
	c.queue.setSongs(songs, reset)
	protect := c.queue.protect
	c.mu.Unlock()
	if protect != nil {
		protect(queueSongIDs(songs), reset)
	}
	c.queueUpdateMu.Unlock()
	c.mu.Lock()
}

func (q *QueueStatus) songResult(id int64, err error) {
	kept := make([]Failure, 0, len(q.Failures)+1)
	for _, f := range q.Failures {
		if f.ID != id {
			kept = append(kept, f)
		}
	}
	if err != nil {
		kept = append(kept, Failure{ID: id, Error: err.Error()})
	}
	q.Failures = kept
	q.refreshErrors()
}

// Keep only failures for songs still in the queue, in their current order.
// This also discards late results from songs removed during a shared download.
func (q *QueueStatus) refreshErrors() {
	byID := make(map[int64]Failure, len(q.Failures))
	for _, f := range q.Failures {
		byID[f.ID] = f
	}
	q.Failures = nil
	var messages []string
	for _, song := range q.Songs {
		if f, ok := byID[song.ID]; ok {
			f.Name = song.Title
			q.Failures = append(q.Failures, f)
			messages = append(messages, fmt.Sprintf("歌曲 %d：%s（最早 60 秒后重试）", f.ID, f.Error))
			delete(byID, song.ID)
		}
	}
	q.Error = strings.Join(messages, "；")
}

func defaultLogDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "LocalLow", "VRChat", "VRChat")
}

func logName(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

func (c *Console) startQueue() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startQueueLocked()
}

// The caller holds lifecycleMu, including when resuming after a batch.
func (c *Console) startQueueLocked() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.httpServer == nil || !c.settings.QueuePrefetchEnabled {
		return errors.New("队列预缓存随本地 CDN 启动")
	}
	if c.queue.Running {
		return nil
	}
	if c.batch.Running && !c.batch.ScanOnly {
		return errors.New("请先停止全曲库批量任务")
	}
	c.mu.Unlock()
	_, err := os.ReadDir(c.settings.LogDir)
	c.mu.Lock()
	if err != nil {
		return fmt.Errorf("无法读取 VRChat 日志目录：%w", err)
	}
	if err := c.ensureEngine(); err != nil {
		return err
	}
	// Establish EOF synchronously so events arriving after start are not missed.
	tail := &vrclog.Tail{Dir: c.settings.LogDir}
	c.mu.Unlock()
	_, err = tail.Poll(time.Now())
	c.mu.Lock()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.queueCancel = cancel
	c.queueDone = make(chan struct{})
	c.queue = QueueStatus{Running: true, File: logName(tail.Path)}
	slog.Info("queue_started", "file", logName(tail.Path))
	go c.runQueue(ctx, tail, c.service, c.queueDone)
	return nil
}

func (c *Console) runQueue(ctx context.Context, tail *vrclog.Tail, engine *cacheproxy.Server, done chan struct{}) {
	defer close(done)
	wake := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); c.queueWorker(ctx, engine, wake) }()
	defer func() {
		<-workerDone
		c.mu.Lock()
		// Clear the old queue before advertising it as stopped. The helper
		// releases mu, so a restart must not replace queue/queueCancel yet.
		c.setQueueSongsLocked(nil, true)
		c.queue.Running = false
		c.queue.Current = 0
		c.queue.Active = nil
		c.queueCancel = nil
		slog.Info("queue_stopped", "completed", c.queue.Completed)
		c.mu.Unlock()
	}()
	ticker := time.NewTicker(vrclog.PollInterval)
	defer ticker.Stop()
	lastActivity := time.Now()
	stale := false
	lastReadError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			previousPath, previousOffset := tail.Path, tail.Offset
			events, err := tail.Poll(now)
			readError := ""
			if err != nil {
				readError = err.Error()
			}
			if readError != lastReadError {
				if err != nil {
					slog.Warn("vrchat_log_read_failed", "error", err)
				} else {
					slog.Info("vrchat_log_read_recovered")
				}
				lastReadError = readError
			}
			if tail.Path != previousPath {
				slog.Info("vrchat_log_changed", "file", logName(tail.Path))
			}
			if tail.Path != previousPath || tail.Offset != previousOffset {
				lastActivity = now
				stale = false
			}
			c.mu.Lock()
			c.queue.File = logName(tail.Path)
			c.queue.LogError = ""
			if err != nil {
				c.queue.LogError = "日志读取失败：" + err.Error()
				c.setQueueSongsLocked(nil, true)
			}
			for _, e := range events {
				c.setQueueSongsLocked(e.Songs, e.Reset)
				c.queue.Updated = now
				ids := make([]int64, len(e.Songs))
				for i, song := range e.Songs {
					ids[i] = song.ID
				}
				slog.Info("queue_updated", "song_count", len(e.Songs), "song_ids", ids, "reset", e.Reset, "generation", c.queue.Generation)
			}
			if now.Sub(lastActivity) >= 5*time.Minute {
				if !stale {
					slog.Warn("vrchat_log_stale")
					c.setQueueSongsLocked(nil, true)
					stale = true
				}
				c.queue.LogError = "日志 5 分钟未更新，已清空待处理队列，等待新的同步"
			}
			c.mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
}

// The coordinator owns deduplication and retry state; workers resolve/download
// at most two songs and recheck the latest queue after resolving the URL.
func (c *Console) queueWorker(ctx context.Context, engine *cacheproxy.Server, wake <-chan struct{}) {
	c.queueUpdateMu.Lock()
	c.mu.Lock()
	c.queue.prefetchCount = c.settings.QueuePrefetchCount
	c.queue.protect = func(ids []int64, reset bool) {
		_ = engine.UpdateQueueSongs(ctx, ids, reset)
	}
	ids := queueSongIDs(c.queue.Songs)
	c.mu.Unlock()
	_ = engine.UpdateQueueSongs(ctx, ids, false)
	c.queueUpdateMu.Unlock()
	type result struct {
		id         int64
		generation uint64
		wanted     bool
		err        error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	defer func() {
		c.mu.Lock()
		for _, waiter := range c.queue.waiters {
			waiter.cancel()
		}
		c.mu.Unlock()
		wg.Wait()
		c.queueUpdateMu.Lock()
		c.mu.Lock()
		c.queue.waiters = nil
		c.queue.protect = nil
		c.mu.Unlock()
		engine.ResetQueueSongs(nil)
		c.queueUpdateMu.Unlock()
	}()
	var generation uint64
	retry := map[int64]time.Time{}
	active := map[int64]bool{}
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if c.queue.Generation != generation {
			generation = c.queue.Generation
			retry = map[int64]time.Time{}
		}
		for i, song := range c.queue.Songs {
			if i >= c.queue.windowSize() || len(active) >= 2 {
				break
			}
			id := song.ID
			if id <= 0 || active[id] || c.queue.prepared[id] || time.Now().Before(retry[id]) {
				continue
			}
			active[id] = true
			gen := generation
			songCtx, cancel := context.WithCancel(applog.WithTrace(ctx))
			slog.Info("queue_song_started", "trace_id", applog.TraceID(songCtx), "song_id", id, "generation", gen)
			if c.queue.waiters == nil {
				c.queue.waiters = make(map[int64]*queueWaiter)
			}
			c.queue.waiters[id] = &queueWaiter{songCtx, cancel, gen}
			wg.Add(1)
			go func() {
				defer wg.Done()
				wanted := func() bool {
					c.mu.Lock()
					defer c.mu.Unlock()
					return c.queue.wants(id, gen)
				}
				_, err := c.prefetchSong(songCtx, engine, id, wanted)
				results <- result{id, gen, !errors.Is(err, errSongRemoved), err}

			}()
		}
		ids := make([]int64, 0, len(active))
		for id := range active {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		c.queue.Active = ids
		c.queue.Current = 0
		if len(ids) > 0 {
			c.queue.Current = ids[0]
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case r := <-results:
			delete(active, r.id)
			c.mu.Lock()
			waiter := c.queue.waiters[r.id]
			valid := waiter.ctx.Err() == nil
			waiter.cancel()
			delete(c.queue.waiters, r.id)
			if valid && r.wanted && c.queue.wants(r.id, r.generation) && ctx.Err() == nil {
				if r.err != nil {
					retry[r.id] = time.Now().Add(time.Minute)
					slog.Warn("queue_song_failed", "trace_id", applog.TraceID(waiter.ctx), "generation", r.generation, "song_id", r.id, "error", r.err, "retry_seconds", 60)
				} else {
					if c.queue.prepared == nil {
						c.queue.prepared = make(map[int64]bool)
					}
					c.queue.prepared[r.id] = true
					c.queue.Completed++
					slog.Info("queue_song_ready", "trace_id", applog.TraceID(waiter.ctx), "generation", r.generation, "song_id", r.id)
				}
				c.queue.songResult(r.id, r.err)
			} else {
				slog.Info("queue_song_discarded", "trace_id", applog.TraceID(waiter.ctx), "song_id", r.id, "generation", r.generation, "reason", "canceled_or_obsolete")
			}
			c.mu.Unlock()
		}
	}
}

// The caller holds lifecycleMu. Queue failures must not stop playback services.
func (c *Console) resumeQueueLocked() {
	c.mu.Lock()
	enabled := c.settings.QueuePrefetchEnabled && c.httpServer != nil && !c.closing && !(c.batch.Running && !c.batch.ScanOnly)
	c.mu.Unlock()
	if enabled {
		err := c.startQueueLocked()
		c.recordActionError("/api/queue/start", err)
		if err != nil {
			slog.Error("queue_start_failed", "error", err)
		}
	}
}
