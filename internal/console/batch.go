package console

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

type Failure struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}
type Batch struct {
	Target         *cacheproxy.DownloadTarget `json:"target,omitempty"`
	Catalog        cacheproxy.CatalogStatus   `json:"catalog"`
	CatalogWarning string                     `json:"catalogWarning"`
	BudgetReached  bool                       `json:"budgetReached"`
	CatalogHits    int                        `json:"catalogHits"`
	Reused         int                        `json:"reused"`
	ScanOnly       bool                       `json:"scanOnly"`
	Missing        int                        `json:"missing"`
	Unresolved     int                        `json:"unresolved"`
	Updated        time.Time                  `json:"updated"`
	Finished       time.Time                  `json:"finished"`
	Running        bool                       `json:"running"`
	Phase          string                     `json:"phase"`
	Total          int                        `json:"total"`
	Checked        int                        `json:"checked"`
	Hits           int                        `json:"hits"`
	Downloaded     int                        `json:"downloaded"`
	Failed         int                        `json:"failed"`
	Skipped        int                        `json:"skipped"`
	Current        string                     `json:"current"`
	Failures       []Failure                  `json:"failures"`
}

func (c *Console) startBatch() error { return c.startBatchMode(false) }

func (c *Console) startBatchMode(scanOnly bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startBatchModeLocked(scanOnly)
}

func (c *Console) startBatchModeLocked(scanOnly bool) error {
	return c.startBatchTargetLocked(scanOnly, nil)
}

func (c *Console) startBatchTarget(storageID string, target cacheproxy.DownloadTarget) error {
	if (target.SongID == "") == (target.Resource == "") {
		return errors.New("请选择一首歌曲或一个资源")
	}
	if target.SongID != "" {
		id, err := strconv.ParseInt(target.SongID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != target.SongID {
			return errors.New("无效歌曲 ID")
		}
	} else if !validLibraryResource(target.Resource) {
		return errors.New("无效资源")
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	current := cacheproxy.CacheStorageID(libraryPaths(c.settings))
	c.mu.Unlock()
	if storageID != current {
		return errors.New("存储目录已切换，请刷新曲库")
	}
	return c.startBatchTargetLocked(false, &target)
}

func (c *Console) startBatchTargetLocked(scanOnly bool, target *cacheproxy.DownloadTarget) error {
	c.mu.Lock()
	closing, running := c.closing, c.batch.Running
	c.mu.Unlock()
	if closing {
		return errors.New("程序正在退出")
	}
	if running {
		return errors.New("已有批量任务正在运行")
	}
	engine, err := c.ensureEngineLocked()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchCancel = cancel
	c.batchDone = make(chan struct{})
	c.batch = Batch{Running: true, ScanOnly: scanOnly, Target: target, Phase: "正在检查远端并更新本地清单"}
	if target != nil {
		c.batch.Phase = "正在读取选中歌曲或资源"
	}
	slog.Info("batch_started", "scan_only", scanOnly)
	go c.runBatch(ctx, engine, c.batchDone)
	return nil
}

func (c *Console) runBatch(ctx context.Context, s *cacheproxy.Server, done chan struct{}) {
	defer close(done)
	started := time.Now()
	completed := false
	c.mu.Lock()
	scanOnly, settings, target := c.batch.ScanOnly, c.settings, c.batch.Target
	c.mu.Unlock()
	fill := s.NewDownloadFill(ctx, scanOnly, settings.RetainFavorites)

	defer func() {
		c.lifecycleMu.Lock()
		defer c.lifecycleMu.Unlock()
		c.mu.Lock()
		result := c.batch
		result.Failures = append([]Failure(nil), c.batch.Failures...)
		c.mu.Unlock()
		result.Running = false
		result.Finished = time.Now()
		if ctx.Err() != nil {
			result.Phase = "任务已停止，上次成功结果保留。已开始的共享下载可能继续完成。"
		}
		successful := target == nil && completed && ctx.Err() == nil && result.Total > 0 && result.Checked == result.Total && result.Failed == 0
		if successful {
			result.Updated = time.Now()
			if err := c.writeSnapshot("batch-last-success", libraryPaths(settings), result); err != nil {
				result.Phase += "；本次成功结果未能保存，当前仍可查看：" + err.Error()
			}
		}
		if err := c.writeSnapshot("batch-last-attempt", libraryPaths(settings), result); err != nil {
			result.Phase += "；本次任务记录未能保存，当前仍可查看：" + err.Error()
		}
		c.mu.Lock()
		if successful {
			c.lastBatch = result
		}
		c.batch = result
		c.batchCancel = nil
		c.mu.Unlock()
		slog.Info("batch_finished", "scan_only", scanOnly, "reused", result.Reused, "completed", completed, "elapsed_ms", time.Since(started).Milliseconds(), "missing", result.Missing, "cancelled", ctx.Err() != nil, "total", result.Total, "checked", result.Checked, "hits", result.Hits, "downloaded", result.Downloaded, "failed", result.Failed, "phase", result.Phase)
	}()

	fail := func(message string) { c.mu.Lock(); c.batch.Phase = message; c.mu.Unlock() }
	var refreshErr, err error
	var local cacheproxy.LocalCatalog
	var release func()
	if target == nil {
		refreshErr = c.refreshLocalCatalog(ctx, s.Store)
		local, release, err = s.ReadLocalCatalog(ctx)
	} else {
		local, release, err = s.ReadDownloadTarget(ctx, *target)
	}
	defer release()
	c.mu.Lock()
	if refreshErr != nil {
		c.batch.CatalogWarning = refreshErr.Error()
	}
	if err == nil && local.Status.Revision != "" && len(local.Songs) > 0 {
		c.batch.Catalog = local.Status
	}
	c.mu.Unlock()
	if err != nil {
		fail("读取本地清单失败：" + err.Error())
		return
	}
	if target != nil && len(local.Songs) == 0 {
		fail("所选歌曲不存在，或资源已无当前歌曲关联。请刷新曲库。")
		return
	}
	if target == nil && (local.Status.Revision == "" || len(local.Songs) == 0) {
		fail("暂无可用的歌曲清单，未开始本地检查。请重试在线更新。")
		return
	}
	c.mu.Lock()
	c.batch.Phase = "正在检查本地清单与本地视频文件"
	c.mu.Unlock()
	songs := local.Songs
	titles := map[string]string{}
	for _, song := range songs {
		name := ""
		if song.Name != nil {
			name = *song.Name
		}
		titles[strconv.FormatInt(song.ID, 10)] = name
	}
	result, err := fill.Run(local, func(ctx context.Context, id int64) (string, error) {
		return c.prefetchSong(ctx, s, id)
	}, func(progress cacheproxy.DownloadFillResult) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.batch.Total, c.batch.Checked = progress.Total, progress.Checked
		c.batch.Hits, c.batch.Downloaded = progress.Hits, progress.Downloaded
		c.batch.Failed, c.batch.Skipped = progress.Failed, progress.Skipped
		c.batch.Missing, c.batch.Reused, c.batch.CatalogHits = progress.Missing, progress.Reused, progress.CatalogHits
		c.batch.Unresolved = progress.Unresolved
		c.batch.BudgetReached = progress.BudgetReached
		c.batch.Current = ""
		if progress.CurrentSongID != "" {
			c.batch.Current = progress.CurrentSongID + " · " + titles[progress.CurrentSongID]
		}
		// Serialized progress only appends failures within this fill.
		for _, failure := range progress.Failures[len(c.batch.Failures):] {
			id, _ := strconv.ParseInt(failure.SongID, 10, 64)
			c.batch.Failures = append(c.batch.Failures, Failure{ID: id, Name: titles[failure.SongID], Error: failure.Err.Error()})
		}
		if scanOnly {
			c.batch.Phase = "正在检查 MD5 文件是否齐备"
		} else {
			c.batch.Phase = "正在解析歌曲并补齐缺失视频"
		}
	})
	if result.BudgetReached {
		c.mu.Lock()
		c.batch.Phase = "视频存储预算不足，已停止普通补齐。"
		if settings.RetainFavorites {
			c.batch.Phase += "本轮可处理的收藏已继续处理。"
		}
		c.batch.Phase += "调整视频保留空间后可再次补齐。"
		c.mu.Unlock()
		return
	}
	if err != nil {
		fail(err.Error())
		return
	}
	if ctx.Err() != nil {
		return
	}
	completed = true
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batch.Current = ""
	if c.batch.Failed > 0 {
		c.batch.Phase = "检查完成，部分歌曲失败；再次检查可重试"
	} else if scanOnly {
		c.batch.Phase = "本地检查完成；可通过下载补齐获取缺失视频"
	} else if target != nil {
		c.batch.Phase = "所选歌曲或资源已处理完成；同 MD5 共用一份视频"
	} else {
		c.batch.Phase = "当前清单歌曲已处理完成；同 MD5 共用一份视频"
	}
}
