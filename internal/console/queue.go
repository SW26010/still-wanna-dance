package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
	Desired   bool              `json:"desired"` // populated from Console user intent in status responses
	Running   bool              `json:"running"`
	File      string            `json:"file"`
	Songs     []vrclog.Song     `json:"songs"`
	Titles    map[string]string `json:"titles,omitempty"` // local catalog, filled for the status response
	Current   int64             `json:"current"`
	Active    []int64           `json:"active"`
	Blocked   []int64           `json:"blocked"`
	Completed int               `json:"completed"`
	Error     string            `json:"error"`
	Failures  []Failure         `json:"failures"`
	LogError  string            `json:"logError"`
	Updated   time.Time         `json:"updated"`
}

// queueTask owns queue state and the ordering between log updates, engine
// synchronization and preparation. Its zero value is ready to use.
type queueTask struct {
	updateMu sync.Mutex // acquired before mu; serializes engine source updates
	mu       sync.Mutex
	QueueStatus
	generation    uint64
	waiters       map[int64]*queueWaiter
	prepared      map[int64]bool
	updateSources func([]int64, bool)
	cancel        context.CancelFunc
	done          chan struct{}
	logFault      func(error) // protected by mu; observes retry results only
}

func (q *queueTask) setLogFaultObserver(observe func(error)) {
	q.mu.Lock()
	q.logFault = observe
	q.mu.Unlock()
}

func (q *queueTask) observeLogFault(err error) {
	q.mu.Lock()
	observe := q.logFault
	q.mu.Unlock()
	if observe != nil {
		observe(err)
	}
}

type queueWaiter struct {
	ctx        context.Context
	cancel     context.CancelFunc
	generation uint64
}

func (q *queueTask) snapshot() QueueStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	status := q.QueueStatus
	status.Songs = append([]vrclog.Song(nil), status.Songs...)
	status.Active = append([]int64(nil), status.Active...)
	status.Blocked = append([]int64(nil), status.Blocked...)
	status.Failures = append([]Failure(nil), status.Failures...)
	status.Titles = maps.Clone(status.Titles)
	return status
}

// requestStop signals cancellation without waiting for source withdrawal.
func (q *queueTask) requestStop() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancel != nil {
		q.cancel()
	}
	return q.done
}

func (q *queueTask) stop() {
	if done := q.requestStop(); done != nil {
		<-done
	}
}

func (q *queueTask) wants(id int64, generation uint64) bool {
	if generation != q.generation {
		return false
	}
	for _, song := range q.Songs {
		if song.ID == id {
			return true
		}
	}
	return false
}

// Call with q.mu held. Reset errors even when the next room has the same IDs.
func (q *queueTask) setSongs(songs []vrclog.Song, reset bool) {
	q.Songs = append([]vrclog.Song(nil), songs...)
	q.Blocked = nil
	if reset {
		q.generation++
		q.Failures = nil
		q.prepared = nil
	}
	// Remember success only while a song stays in the queue. Do
	// this on every snapshot, including snapshots coalesced before a wake.
	// Reentry checks the current version again; the engine manages protection
	// from registered usage and its local protection window.
	for id := range q.prepared {
		if !q.wants(id, q.generation) {
			delete(q.prepared, id)
		}
	}
	// Cancel the caller synchronously with invalidation, before a newly freed
	// engine slot can admit an obsolete waiter. Existing flights own their context.
	for id, waiter := range q.waiters {
		if !q.wants(id, waiter.generation) {
			if waiter.ctx.Err() == nil {
				slog.Info("队列歌曲等待已取消", "event_name", "queue_song_canceled", "trace_id", applog.TraceID(waiter.ctx), "song_id", id, "generation", waiter.generation, "reason", "queue_changed")
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

// Serialize the state change and engine protection without holding mu while
// protection waits. A later reset must not overtake an earlier update.
func (q *queueTask) setQueueSongs(songs []vrclog.Song, reset bool) {
	q.updateMu.Lock()
	defer q.updateMu.Unlock()
	q.mu.Lock()
	q.setSongs(songs, reset)
	updateSources := q.updateSources
	q.mu.Unlock()
	updateQueueSources(updateSources, songs, reset)
}

func updateQueueSources(updateSources func([]int64, bool), songs []vrclog.Song, reset bool) {
	if updateSources != nil {
		updateSources(queueSongIDs(songs), reset)
	}
}

func (q *queueTask) songResult(id int64, err error) {
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
func (q *queueTask) refreshErrors() {
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

// Resolve at use sites; an empty setting must remain automatic when
// round-tripped through the API or saved on another user's machine.
func (s Settings) effectiveVRChatLogDir() string {
	if s.VRChatLogDir == "" {
		return defaultLogDir()
	}
	return s.VRChatLogDir
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
	c.mu.Unlock()
	c.queue.stop()
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
	closing, logDir := c.closing, c.settings.effectiveVRChatLogDir()
	c.mu.Unlock()
	if closing {
		return rejectRequest(errors.New("程序正在退出"))
	}
	if c.queue.snapshot().Running {
		return nil
	}
	_, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("无法读取 VRChat 日志目录：%w", err)
	}
	engine, err := c.ensureEngineLocked()
	if err != nil {
		return err
	}
	// Establish EOF synchronously so events arriving after start are not missed.
	tail := &vrclog.Tail{Dir: logDir}
	_, err = tail.Poll(time.Now())
	if err != nil {
		return err
	}
	c.queue.start(tail, engine)
	return nil
}

// Starts and stops are serialized by the Console lifecycle.
func (q *queueTask) start(tail *vrclog.Tail, engine *cacheproxy.Server) {
	ctx, cancel := context.WithCancel(context.Background())
	q.mu.Lock()
	q.cancel = cancel
	q.done = make(chan struct{})
	q.QueueStatus = QueueStatus{Running: true, File: logName(tail.Path)}
	q.generation = 0
	q.waiters = nil
	q.prepared = nil
	q.updateSources = nil
	done := q.done
	q.mu.Unlock()
	slog.Info("房间队列监控已开始", "event_name", "queue_started", "file", logName(tail.Path))
	go q.runQueue(ctx, tail, engine, done)
}

func (q *queueTask) runQueue(ctx context.Context, tail *vrclog.Tail, engine *cacheproxy.Server, done chan struct{}) {
	defer close(done)
	defer q.observeLogFault(nil)
	wake := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); q.queueWorker(ctx, engine, wake) }()
	defer func() {
		<-workerDone
		// Clear the old queue before advertising it as stopped. The lifecycle
		// waits for done before allowing a restart to replace this queue.
		q.setQueueSongs(nil, true)
		q.mu.Lock()
		q.Running = false
		q.Current = 0
		q.Active = nil
		q.cancel = nil
		slog.Info("房间队列监控已停止", "event_name", "queue_stopped", "completed", q.Completed)
		q.mu.Unlock()
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
					slog.Warn("VRChat 日志读取失败", "event_name", "vrchat_log_read_failed", "error", err)
				} else {
					slog.Info("VRChat 日志读取已恢复", "event_name", "vrchat_log_read_recovered")
				}
				lastReadError = readError
			}
			if tail.Path != previousPath {
				slog.Info("已切换 VRChat 日志文件", "event_name", "vrchat_log_changed", "file", logName(tail.Path))
			}
			if tail.Path != previousPath || tail.Offset != previousOffset {
				lastActivity = now
				stale = false
			}
			// Include synthetic resets in the same ordered batch, but update
			// the observed timestamp only for snapshots from the log.
			updates := events
			firstEvent := 0
			if err != nil {
				updates = make([]vrclog.Event, 0, len(events)+2)
				updates = append(updates, vrclog.Event{Reset: true})
				updates = append(updates, events...)
				firstEvent = 1
			}
			eventEnd := len(updates)
			// An idle log can simply mean VRChat is not running. Withdraw
			// old work without turning inactivity into a read error.
			if now.Sub(lastActivity) >= 5*time.Minute && !stale {
				updates = append(updates, vrclog.Event{Reset: true})
			}
			// Keep one update lock across the whole batch so the worker cannot
			// admit an intermediate queue between successive log snapshots.
			q.updateMu.Lock()
			q.mu.Lock()
			q.File = logName(tail.Path)
			q.LogError = ""
			if err != nil {
				q.LogError = "日志读取失败：" + err.Error()
			}
			for i, e := range updates {
				if i == eventEnd {
					slog.Warn("VRChat 日志暂无新内容", "event_name", "vrchat_log_stale")
					stale = true
				}
				q.setSongs(e.Songs, e.Reset)
				updateSources := q.updateSources
				q.mu.Unlock()
				updateQueueSources(updateSources, e.Songs, e.Reset)
				q.mu.Lock()
				if i >= firstEvent && i < eventEnd {
					q.Updated = now
					slog.Info("房间播放队列已更新", "event_name", "queue_updated", "song_count", len(e.Songs), "song_ids", queueSongIDs(e.Songs), "reset", e.Reset, "generation", q.generation)
				}
			}
			q.mu.Unlock()
			q.updateMu.Unlock()
			q.observeLogFault(err)
		}
	}
}

// The coordinator admits sources in queue order. Each admitted preparation
// reports its result independently; completion never holds a song-level slot.
func (q *queueTask) queueWorker(ctx context.Context, engine *cacheproxy.Server, wake chan struct{}) {
	q.updateMu.Lock()
	q.mu.Lock()
	q.updateSources = func(ids []int64, reset bool) {
		if err := engine.UpdatePrefetchQueue(ids, reset); err != nil {
			slog.Warn("队列播放时间推算失败", "event_name", "queue_timing_failed", "error", err)
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	ids := queueSongIDs(q.Songs)
	q.mu.Unlock()
	if err := engine.UpdatePrefetchQueue(ids, false); err != nil {
		slog.Warn("队列播放时间推算失败", "event_name", "queue_timing_failed", "error", err)
	}
	q.updateMu.Unlock()
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
		q.mu.Lock()
		for _, waiter := range q.waiters {
			waiter.cancel()
		}
		q.mu.Unlock()
		wg.Wait()
		q.updateMu.Lock()
		q.mu.Lock()
		q.waiters = nil
		q.updateSources = nil
		q.mu.Unlock()
		engine.UpdatePrefetchQueue(nil, true)
		q.updateMu.Unlock()
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
		policies, policyErr := engine.Store.SongPolicies(ctx)
		if policyErr != nil && ctx.Err() == nil {
			slog.Warn("队列歌曲偏好读取失败", "event_name", "queue_song_preferences_failed", "error", policyErr)
		}
		// The visible snapshot can precede the engine's metadata/source update.
		// Dispatch only after that same serialized update has finished.
		q.updateMu.Lock()
		q.mu.Lock()
		q.Blocked = nil
		for _, song := range q.Songs {
			if policies[song.ID] == cacheproxy.SongBlocked {
				q.Blocked = append(q.Blocked, song.ID)
				q.songResult(song.ID, nil)
				delete(retry, song.ID)
			}
		}
		if q.generation != generation {
			generation = q.generation
			retry = map[int64]time.Time{}
		}
		// A successful preparation can later lose its usage on refetch expiry.
		// Reuse the normal ordered admission once the engine rearms the member.
		for id := range q.prepared {
			if engine.QueueNeedsPreparation(id) {
				delete(q.prepared, id)
			}
		}
		now := time.Now()
		// Only the unresolved/unadmitted next source waits here. Already
		// admitted resources keep downloading and reporting independently.
		if admission != nil {
			waiter := q.waiters[admitting]
			if waiter == nil || waiter.ctx.Err() != nil {
				admission, admitting = nil, 0
			} else {
				select {
				case <-admission:
					admission, admitting = nil, 0
				default:
					// An earlier newly inserted source takes the pending place;
					// already admitted sources need no caller replacement.
					for _, song := range q.Songs {
						if song.ID == admitting {
							break
						}
						if song.ID > 0 && policies[song.ID] != cacheproxy.SongBlocked && !active[song.ID] && !q.prepared[song.ID] && !now.Before(retry[song.ID]) {
							waiter.cancel()
							admission, admitting = nil, 0
							break
						}
					}
				}
			}
		}
		for _, song := range q.Songs {
			if admission != nil {
				break
			}
			id := song.ID
			if id <= 0 || policies[id] == cacheproxy.SongBlocked || active[id] || q.prepared[id] || now.Before(retry[id]) {
				continue
			}
			active[id] = true
			delete(retry, id)
			gen := generation
			songCtx, cancel := context.WithCancel(applog.WithTrace(ctx))
			songCtx, admission = cacheproxy.WithPreparationAdmission(songCtx)
			admitting = id
			slog.Info("队列歌曲准备已开始", "event_name", "queue_song_started", "trace_id", applog.TraceID(songCtx), "song_id", id, "generation", gen)
			if q.waiters == nil {
				q.waiters = make(map[int64]*queueWaiter)
			}
			q.waiters[id] = &queueWaiter{songCtx, cancel, gen}
			wg.Add(1)
			go func() {
				defer wg.Done()
				wanted := func() bool {
					q.mu.Lock()
					defer q.mu.Unlock()
					return q.wants(id, gen)
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
		q.Active = ids
		q.Current = 0
		if len(ids) > 0 {
			q.Current = ids[0]
		}
		// Already-due entries held behind admission wait for that operation's
		// signal, rather than repeatedly arming a zero-delay timer.
		var nextRetry time.Time
		for _, song := range q.Songs {
			id, due := song.ID, retry[song.ID]
			if policies[id] != cacheproxy.SongBlocked && !active[id] && !q.prepared[id] && due.After(now) && (nextRetry.IsZero() || due.Before(nextRetry)) {
				nextRetry = due
			}
		}
		q.mu.Unlock()
		q.updateMu.Unlock()
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
			q.mu.Lock()
			waiter := q.waiters[r.id]
			valid := waiter.ctx.Err() == nil
			waiter.cancel()
			delete(q.waiters, r.id)
			if valid && r.wanted && q.wants(r.id, r.generation) && ctx.Err() == nil {
				if errors.Is(r.err, cacheproxy.ErrSongBlocked) {
					delete(retry, r.id)
					r.err = nil
				} else if r.err != nil {
					retry[r.id] = time.Now().Add(time.Minute)
					slog.Warn("队列歌曲准备失败", "event_name", "queue_song_failed", "trace_id", applog.TraceID(waiter.ctx), "generation", r.generation, "song_id", r.id, "error", r.err, "retry_seconds", 60)
				} else {
					if q.prepared == nil {
						q.prepared = make(map[int64]bool)
					}
					q.prepared[r.id] = true
					q.Completed++
					slog.Info("队列歌曲已准备就绪", "event_name", "queue_song_ready", "trace_id", applog.TraceID(waiter.ctx), "generation", r.generation, "song_id", r.id)
				}
				q.songResult(r.id, r.err)
			} else {
				slog.Info("队列歌曲过期结果已丢弃", "event_name", "queue_song_discarded", "trace_id", applog.TraceID(waiter.ctx), "song_id", r.id, "generation", r.generation, "reason", "canceled_or_obsolete")
			}
			q.mu.Unlock()
		}
	}
}
