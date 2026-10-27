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
	Desired    bool              `json:"desired"` // populated from Console user intent in status responses
	Running    bool              `json:"running"`
	File       string            `json:"file"`
	Songs      []vrclog.Song     `json:"songs"`
	Titles     map[string]string `json:"titles,omitempty"` // local catalog, filled for the status response
	Current    int64             `json:"current"`
	Active     []int64           `json:"active"`
	Blocked    []int64           `json:"blocked"`
	Completed  int               `json:"completed"`
	Error      string            `json:"error"`
	Failures   []Failure         `json:"failures"`
	LogError   string            `json:"logError"`
	Updated    time.Time         `json:"updated"`
	Generation uint64            `json:"-"`
	waiters    map[int64]*queueWaiter
	prepared   map[int64]bool

	updateSources func([]int64, bool)
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
	for _, song := range q.Songs {
		if song.ID == id {
			return true
		}
	}
	return false
}

// Call with c.mu held. Reset errors even when the next room has the same IDs.
func (q *QueueStatus) setSongs(songs []vrclog.Song, reset bool) {
	q.Songs = songs
	q.Blocked = nil
	if reset {
		q.Generation++
		q.Failures = nil
		q.prepared = nil
	}
	// Remember success only while a song stays in the queue. Do
	// this on every snapshot, including snapshots coalesced before a wake.
	// Reentry checks the current version again; the engine manages protection
	// from registered usage and its local protection window.
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
		ids = append(ids, song.ID)
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
	c.updateQueueSongsLocked(songs, reset)
	c.queueUpdateMu.Unlock()
}

// Both locks are held on entry/return. The log reader keeps queueUpdateMu across
// one batch of snapshots, so notifications cannot admit an intermediate queue.
func (c *Console) updateQueueSongsLocked(songs []vrclog.Song, reset bool) {
	c.queue.setSongs(songs, reset)
	updateSources := c.queue.updateSources
	c.mu.Unlock()
	if updateSources != nil {
		updateSources(queueSongIDs(songs), reset)
	}
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
			f.Name = "" // Enriched from the engine's catalog snapshot when shown.
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

// Caller holds lifecycleMu. Wait until queue sources have been withdrawn.
func (c *Console) stopQueueLocked() {
	c.mu.Lock()
	c.queueDesired = false
	if c.queueCancel != nil {
		c.queueCancel()
	}
	done := c.queueDone
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Follow the gateway without making log-directory failures acceleration failures.
// Caller holds lifecycleMu, not mu. Repeated acceleration can retry a failed queue.
func (c *Console) startGatewayQueueLocked() {
	if !c.settings.QueueWithGateway {
		return
	}
	c.mu.Lock()
	c.queueDesired = true
	c.mu.Unlock()
	c.recordActionError("queue", c.startQueueLocked())
}

// The caller holds lifecycleMu. Manual downloads share the demand scheduler.
func (c *Console) startQueueLocked() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("程序正在退出")
	}
	if c.queue.Running {
		return nil
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
			c.queueUpdateMu.Lock()
			c.mu.Lock()
			c.queue.File = logName(tail.Path)
			c.queue.LogError = ""
			if err != nil {
				c.queue.LogError = "日志读取失败：" + err.Error()
				c.updateQueueSongsLocked(nil, true)
			}
			for _, e := range events {
				c.updateQueueSongsLocked(e.Songs, e.Reset)
				c.queue.Updated = now
				ids := make([]int64, len(e.Songs))
				for i, song := range e.Songs {
					ids[i] = song.ID
				}
				slog.Info("queue_updated", "song_count", len(e.Songs), "song_ids", ids, "reset", e.Reset, "generation", c.queue.Generation)
			}
			// An idle log can simply mean VRChat is not running. Withdraw
			// old work without turning inactivity into a read error.
			if now.Sub(lastActivity) >= 5*time.Minute {
				if !stale {
					slog.Warn("vrchat_log_stale")
					c.updateQueueSongsLocked(nil, true)
					stale = true
				}
			}
			c.mu.Unlock()
			c.queueUpdateMu.Unlock()
		}
	}
}

// The coordinator admits sources in queue order. Each admitted preparation
// reports its result independently; completion never holds a song-level slot.
func (c *Console) queueWorker(ctx context.Context, engine *cacheproxy.Server, wake chan struct{}) {
	c.queueUpdateMu.Lock()
	c.mu.Lock()
	c.queue.updateSources = func(ids []int64, reset bool) {
		if err := engine.UpdatePrefetchQueue(ids, reset); err != nil {
			slog.Warn("queue_timing_failed", "error", err)
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	ids := queueSongIDs(c.queue.Songs)
	c.mu.Unlock()
	if err := engine.UpdatePrefetchQueue(ids, false); err != nil {
		slog.Warn("queue_timing_failed", "error", err)
	}
	c.queueUpdateMu.Unlock()
	type result struct {
		id         int64
		generation uint64
		wanted     bool
		err        error
	}
	results := make(chan result)
	var admitting int64
	var admission <-chan struct{}
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
		c.queue.updateSources = nil
		c.mu.Unlock()
		engine.UpdatePrefetchQueue(nil, true)
		c.queueUpdateMu.Unlock()
	}()
	var generation uint64
	retry := map[int64]time.Time{}
	active := map[int64]bool{}
	retryTimer := time.NewTimer(time.Minute)
	retryTimer.Stop()
	defer retryTimer.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		changed := engine.QueuePreparationChanged()
		policies, policyErr := engine.SongPolicies(ctx)
		if policyErr != nil && ctx.Err() == nil {
			slog.Warn("queue_song_preferences_failed", "error", policyErr)
		}
		// The visible snapshot can precede the engine's metadata/source update.
		// Dispatch only after that same serialized update has finished.
		c.queueUpdateMu.Lock()
		c.mu.Lock()
		c.queue.Blocked = nil
		for _, song := range c.queue.Songs {
			if policies[song.ID] == cacheproxy.SongBlocked {
				c.queue.Blocked = append(c.queue.Blocked, song.ID)
				c.queue.songResult(song.ID, nil)
				delete(retry, song.ID)
			}
		}
		if c.queue.Generation != generation {
			generation = c.queue.Generation
			retry = map[int64]time.Time{}
		}
		// A successful preparation can later lose its usage on refetch expiry.
		// Reuse the normal ordered admission once the engine rearms the member.
		for id := range c.queue.prepared {
			if engine.QueueNeedsPreparation(id) {
				delete(c.queue.prepared, id)
			}
		}
		now := time.Now()
		// Only the unresolved/unadmitted next source waits here. Already
		// admitted resources keep downloading and reporting independently.
		if admission != nil {
			waiter := c.queue.waiters[admitting]
			if waiter == nil || waiter.ctx.Err() != nil {
				admission, admitting = nil, 0
			} else {
				select {
				case <-admission:
					admission, admitting = nil, 0
				default:
					// An earlier newly inserted source takes the pending place;
					// already admitted sources need no caller replacement.
					for _, song := range c.queue.Songs {
						if song.ID == admitting {
							break
						}
						if song.ID > 0 && policies[song.ID] != cacheproxy.SongBlocked && !active[song.ID] && !c.queue.prepared[song.ID] && !now.Before(retry[song.ID]) {
							waiter.cancel()
							admission, admitting = nil, 0
							break
						}
					}
				}
			}
		}
		for _, song := range c.queue.Songs {
			if admission != nil {
				break
			}
			id := song.ID
			if id <= 0 || policies[id] == cacheproxy.SongBlocked || active[id] || c.queue.prepared[id] || now.Before(retry[id]) {
				continue
			}
			active[id] = true
			delete(retry, id)
			gen := generation
			songCtx, cancel := context.WithCancel(applog.WithTrace(ctx))
			songCtx, admission = cacheproxy.WithPreparationAdmission(songCtx)
			admitting = id
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
				var err error
				if !wanted() {
					err = errSongRemoved
				} else {
					_, err = engine.PrefetchQueuePlayback(songCtx, id)
				}
				select {
				case results <- result{id, gen, !errors.Is(err, errSongRemoved), err}:
				case <-ctx.Done():
				}

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
		// Already-due entries held behind admission wait for that operation's
		// signal, rather than repeatedly arming a zero-delay timer.
		var nextRetry time.Time
		for _, song := range c.queue.Songs {
			id, due := song.ID, retry[song.ID]
			if policies[id] != cacheproxy.SongBlocked && !active[id] && !c.queue.prepared[id] && due.After(now) && (nextRetry.IsZero() || due.Before(nextRetry)) {
				nextRetry = due
			}
		}
		c.mu.Unlock()
		c.queueUpdateMu.Unlock()
		retryTimer.Stop()
		var retryDue <-chan time.Time
		if !nextRetry.IsZero() {
			retryTimer.Reset(time.Until(nextRetry))
			retryDue = retryTimer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-changed:
		case <-retryDue:
		case <-admission:
			admission, admitting = nil, 0
		case r := <-results:
			if admitting == r.id {
				admission, admitting = nil, 0
			}
			delete(active, r.id)
			c.mu.Lock()
			waiter := c.queue.waiters[r.id]
			valid := waiter.ctx.Err() == nil
			waiter.cancel()
			delete(c.queue.waiters, r.id)
			if valid && r.wanted && c.queue.wants(r.id, r.generation) && ctx.Err() == nil {
				if errors.Is(r.err, cacheproxy.ErrSongBlocked) {
					delete(retry, r.id)
					r.err = nil
				} else if r.err != nil {
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
