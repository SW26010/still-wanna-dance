package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stepstash/internal/cacheproxy"
	"stepstash/internal/vrclog"
)

type QueueStatus struct {
	Running    bool          `json:"running"`
	File       string        `json:"file"`
	Songs      []vrclog.Song `json:"songs"`
	Current    int64         `json:"current"`
	Completed  int           `json:"completed"`
	Error      string        `json:"error"`
	Failures   []Failure     `json:"failures"`
	LogError   string        `json:"logError"`
	Updated    time.Time     `json:"updated"`
	Generation uint64        `json:"-"`
}

// Call with c.mu held. Reset errors even when the next room has the same IDs.
func (q *QueueStatus) setSongs(songs []vrclog.Song, reset bool) {
	q.Songs = songs
	if reset {
		q.Generation++
		q.Failures = nil
	}
	q.refreshErrors()
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.queue.Running {
		return nil
	}
	if c.batch.Running && !c.batch.ScanOnly {
		return errors.New("请先停止全曲库批量任务")
	}
	if _, err := os.ReadDir(c.settings.LogDir); err != nil {
		return fmt.Errorf("无法读取 VRChat 日志目录：%w", err)
	}
	if err := c.ensureEngine(); err != nil {
		return err
	}
	// Establish EOF synchronously so events arriving after start are not missed.
	tail := &vrclog.Tail{Dir: c.settings.LogDir}
	if _, err := tail.Poll(time.Now()); err != nil {
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
		c.queue.Running = false
		c.queue.Current = 0
		c.queue.setSongs(nil, true)
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
				c.queue.setSongs(nil, true)
			}
			for _, e := range events {
				c.queue.setSongs(e.Songs, e.Reset)
				c.queue.Updated = now
				slog.Info("queue_updated", "song_count", len(e.Songs), "reset", e.Reset)
			}
			if now.Sub(lastActivity) >= 5*time.Minute {
				if !stale {
					slog.Warn("vrchat_log_stale")
					c.queue.setSongs(nil, true)
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

// One worker rechecks the latest snapshot before starting each song. Finishing
// an already downloading, removed song preserves shared playback downloads.
func (c *Console) queueWorker(ctx context.Context, engine *cacheproxy.Server, wake <-chan struct{}) {
	var generation uint64
	completed := map[int64]bool{}
	retry := map[int64]time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if c.queue.Generation != generation {
			generation = c.queue.Generation
			completed = map[int64]bool{}
			retry = map[int64]time.Time{}
		}
		var id int64
		for i, song := range c.queue.Songs {
			if i >= 3 {
				break
			}
			if song.ID > 0 && !completed[song.ID] && !time.Now().Before(retry[song.ID]) {
				id = song.ID
				break
			}
		}
		c.queue.Current = id
		c.mu.Unlock()
		if id == 0 {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				continue
			}
		}
		target, err := c.resolve(ctx, id)
		// Recheck after the potentially slow API request.
		c.mu.Lock()
		wanted := false
		if generation == c.queue.Generation {
			for i, song := range c.queue.Songs {
				if i >= 3 {
					break
				}
				if song.ID == id {
					wanted = true
				}
			}
		}
		c.mu.Unlock()
		if !wanted {
			continue
		}
		if err == nil && ctx.Err() == nil {
			_, err = engine.Prefetch(ctx, target)
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if generation == c.queue.Generation {
			if err != nil {
				retry[id] = time.Now().Add(time.Minute)
				slog.Warn("queue_song_failed", "song_id", id, "error", err, "retry_seconds", 60)
			} else {
				completed[id] = true
				c.queue.Completed++
				slog.Info("queue_song_ready", "song_id", id)
			}
			c.queue.songResult(id, err)
		}
		c.mu.Unlock()
	}
}
