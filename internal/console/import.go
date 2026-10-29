package console

import (
	"context"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
)

type ImportStatus struct {
	ID               uint64 `json:"id"`
	Present          bool   `json:"present"`
	Running          bool   `json:"running"`
	Paused           bool   `json:"paused"`
	ScopeMismatch    bool   `json:"scopeMismatch"`
	Source           string `json:"source"`
	VideoDir         string `json:"videoDir"`
	Phase            string `json:"phase"`
	Total            int    `json:"total"`
	Checked          int    `json:"checked"`
	Imported         int    `json:"imported"`
	Skipped          int    `json:"skipped"`
	Failed           int    `json:"failed"`
	Deleted          int    `json:"deleted"`
	CleanupAvailable bool   `json:"cleanupAvailable"`
	Error            string `json:"error"`
	Detail           string `json:"detail"`
}

func insideImportRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func enumerateImport(ctx context.Context, source, storage string) ([]videoImportItem, error) {
	var files []videoImportItem
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && insideImportRoot(storage, path) {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4", ".mkv", ".webm", ".avi", ".mov", ".m4v", ".wmv", ".flv", ".mpg", ".mpeg", ".ts", ".m2ts", ".mts", ".3gp", ".ogv", ".vob":
			snapshot, err := cacheproxy.SnapshotImportFile(path)
			if errors.Is(err, cacheproxy.ErrImportSourceChanged) {
				files = append(files, videoImportItem{File: cacheproxy.ImportFile{Source: path}, Result: "skipped", Reason: err.Error()})
				return nil
			}
			if err != nil {
				return err
			}
			files = append(files, videoImportItem{File: snapshot})
		}
		return nil
	})
	return files, err
}

func (c *Console) startImport(source string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	closing, present, videoDir := c.closing, c.importStatus.Present, c.settings.VideoDir
	c.mu.Unlock()
	if closing || present {
		return errors.New("请先结束或放弃当前导入任务")
	}
	if strings.TrimSpace(source) == "" {
		return errors.New("请指定外部目录")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("请选择目录")
	}
	if _, err = cacheproxy.CacheDirectory(videoDir); err != nil {
		return err
	}
	storage, err := filepath.EvalSymlinks(videoDir)
	if err != nil {
		return err
	}
	storage, err = filepath.Abs(storage)
	if err != nil {
		return err
	}
	if insideImportRoot(storage, source) {
		return errors.New("请选择完整视频目录之外的来源目录")
	}
	engine, err := c.ensureEngineLocked()
	if err != nil {
		return err
	}
	paths := libraryPaths(c.settings)
	task := videoImportTask{Version: 1, ID: (rand.Uint64() >> 12) + 1, Source: source, DatabasePath: paths.DatabasePath, VideoDir: paths.VideoDir}
	if err := c.saveImportTask(task); err != nil {
		return err
	}
	c.launchImport(engine, task, false)
	return nil
}

// lifecycleMu serializes admission. The worker owns its copy; mu only protects
// short publications of durable progress and never covers file I/O.
func (c *Console) launchImport(engine *cacheproxy.Server, task videoImportTask, cleanup bool) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.publishImportTask(task)
	c.mu.Lock()
	c.importCancel, c.importDone = cancel, done
	c.importStatus.Running = true
	c.importStatus.Paused = false
	c.importStatus.CleanupAvailable = false
	c.importStatus.Error = ""
	c.importStatus.Phase = "正在读取已知资源"
	if cleanup {
		c.importStatus.Phase = "正在清理已导入的源文件"
	}
	c.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		var err error
		if cleanup {
			err = c.runImportCleanup(ctx, engine, task)
		} else {
			err = c.runImport(ctx, engine, task)
		}
		c.mu.Lock()
		c.importCancel = nil
		status := c.importTask.status(c.settings)
		if cleanup {
			status.Phase = "清理结束"
			if status.CleanupAvailable {
				status.Phase = "部分源文件未清理，可再次确认后重试"
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			status.Error = err.Error()
		}
		if errors.Is(err, context.Canceled) && cleanup {
			status.Phase = "清理已暂停，继续清理需要重新确认"
		}
		c.importStatus = status
		c.mu.Unlock()
	}()
}

func (c *Console) resumeImport(id uint64) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	task, err := c.idleImport(id)
	if err != nil {
		return err
	}
	if !task.status(c.settings).Paused {
		return errors.New("本次导入已经完成")
	}
	engine, err := c.ensureEngineLocked()
	if err != nil {
		return err
	}
	c.launchImport(engine, task, false)
	return nil
}

func (c *Console) pauseImport(id uint64) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || id != c.importStatus.ID || !c.importStatus.Running || c.importCancel == nil {
		return errors.New("本次导入无法暂停，请刷新状态")
	}
	c.importStatus.Phase = "正在暂停"
	c.importCancel()
	return nil
}

func (c *Console) discardImport(id uint64, confirmed bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	valid := !c.closing && c.importStatus.Present && !c.importStatus.Running && id == c.importStatus.ID
	c.mu.Unlock()
	if !confirmed || !valid {
		return errors.New("请确认结束本次导入并刷新状态")
	}
	if err := os.Remove(c.files().State("video-import")); err != nil && !os.IsNotExist(err) {
		return err
	}
	c.mu.Lock()
	c.importTask, c.importStatus = nil, ImportStatus{}
	c.mu.Unlock()
	return nil
}

func (c *Console) idleImport(id uint64) (videoImportTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.importStatus.Running || c.importTask == nil || id != c.importTask.ID {
		return videoImportTask{}, errors.New("本次导入不可操作，请刷新状态")
	}
	if c.importTask.status(c.settings).ScopeMismatch {
		return videoImportTask{}, errors.New("本次导入的数据位置与当前生效位置不一致，请恢复原位置后重启，或放弃本次导入")
	}
	return c.importTask.clone(), nil
}

func (c *Console) runImport(ctx context.Context, engine *cacheproxy.Server, task videoImportTask) error {
	allowed, err := engine.KnownMedia(ctx)
	if err != nil {
		return err
	}
	if len(allowed) == 0 {
		return errors.New("本地尚无已知资源，未开始导入，请先更新曲库")
	}
	if !task.Enumerated {
		c.mu.Lock()
		c.importStatus.Phase = "正在枚举视频文件"
		c.mu.Unlock()
		storage, err := filepath.EvalSymlinks(task.VideoDir)
		if err != nil {
			return err
		}
		files, err := enumerateImport(ctx, task.Source, storage)
		if err != nil {
			return err
		}
		task.Files, task.Enumerated = files, true
		if err := c.commitImportTask(task); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.importStatus.Phase = "正在校验并导入"
	c.mu.Unlock()
	for i := range task.Files {
		item := &task.Files[i]
		if item.Result != "" && item.Result != "failed" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		receipt, err := engine.ImportSnapshotVideo(ctx, item.File, allowed)
		if errors.Is(err, context.Canceled) {
			return err
		}
		item.Reason, item.Receipt = "", receipt
		switch {
		case errors.Is(err, cacheproxy.ErrImportSourceChanged):
			item.Result, item.Reason = "skipped", err.Error()
		case err != nil:
			item.Result, item.Reason = "failed", err.Error()
		case receipt == nil:
			item.Result, item.Reason = "skipped", "目标已存在、资源未知或已屏蔽"
		default:
			item.Result = "imported"
		}
		// Even if pause arrives after publication, first commit its receipt.
		if err := c.commitImportTask(task); err != nil {
			return err
		}
	}
	return nil
}

func (c *Console) cleanupImport(id uint64, confirmed bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if !confirmed {
		return errors.New("删除不可逆，必须再次确认")
	}
	task, err := c.idleImport(id)
	if err != nil {
		return err
	}
	if !task.status(c.settings).CleanupAvailable {
		return errors.New("本次导入不可清理，请刷新状态")
	}
	engine, err := c.ensureEngineLocked()
	if err != nil {
		return err
	}
	c.launchImport(engine, task, true)
	return nil
}

func (c *Console) runImportCleanup(ctx context.Context, engine *cacheproxy.Server, task videoImportTask) error {
	for i := range task.Files {
		item := &task.Files[i]
		if item.Receipt == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err := engine.RemoveImportedSource(ctx, *item.Receipt)
		if errors.Is(err, context.Canceled) {
			return err
		}
		item.CleanupError = ""
		switch {
		case err == nil:
			item.Cleanup, item.Receipt = "deleted", nil
		case errors.Is(err, cacheproxy.ErrImportedSourceMissing):
			item.Cleanup, item.Receipt, item.CleanupError = "missing", nil, err.Error()
		case errors.Is(err, cacheproxy.ErrImportSourceChanged):
			item.Cleanup, item.Receipt, item.CleanupError = "changed", nil, err.Error()
		default:
			item.CleanupError = err.Error()
		}
		if err := c.commitImportTask(task); err != nil {
			return err
		}
	}
	return nil
}
