package console

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
)

type ImportStatus struct {
	ID               uint64 `json:"id"`
	Running          bool   `json:"running"`
	Phase            string `json:"phase"`
	Total            int    `json:"total"`
	Checked          int    `json:"checked"`
	Imported         int    `json:"imported"`
	Skipped          int    `json:"skipped"`
	Failed           int    `json:"failed"`
	Deleted          int    `json:"deleted"`
	CleanupAvailable bool   `json:"cleanupAvailable"`
	Error            string `json:"error"`
}

func insideImportRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func enumerateImport(ctx context.Context, source, storage string) ([]string, error) {
	var files []string
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
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func (c *Console) startImport(source string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.importStatus.Running {
		return errors.New("导入任务正在运行或应用正在退出")
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
	if err = c.ensureEngine(); err != nil {
		return err
	}
	storage, err := filepath.EvalSymlinks(c.settings.StorageDir)
	if err != nil {
		return err
	}
	storage, err = filepath.Abs(storage)
	if err != nil {
		return err
	}
	if insideImportRoot(storage, source) {
		return errors.New("请选择本应用数据目录之外的目录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.importCancel = cancel
	c.importDone = make(chan struct{})
	c.importReceipts = nil
	c.importStatus = ImportStatus{ID: c.importStatus.ID + 1, Running: true, Phase: "正在更新歌曲清单"}
	go c.runImport(ctx, c.service, source, storage, c.importDone)
	return nil
}

func (c *Console) runImport(ctx context.Context, engine *cacheproxy.Server, source, storage string, done chan struct{}) {
	defer close(done)
	defer func() {
		c.mu.Lock()
		c.importStatus.Running = false
		c.importStatus.CleanupAvailable = len(c.importReceipts) > 0
		if c.importCancel != nil {
			c.importCancel()
		}
		c.importCancel = nil
		c.mu.Unlock()
	}()
	fail := func(err error) {
		c.mu.Lock()
		c.importStatus.Error = err.Error()
		c.importStatus.Phase = "导入未完成"
		c.mu.Unlock()
	}
	if err := c.refreshLocalCatalog(ctx, engine); err != nil {
		fail(fmt.Errorf("无法更新歌曲清单，未开始导入：%w", err))
		return
	}
	catalog, release, err := engine.ReadLocalCatalog(ctx)
	defer release()
	if err != nil {
		fail(err)
		return
	}
	allowed := map[string]bool{}
	for _, song := range catalog.Songs {
		if catalog.Current[strconv.FormatInt(song.ID, 10)] {
			allowed[song.MD5] = true
		}
	}
	c.mu.Lock()
	c.importStatus.Phase = "正在枚举视频文件"
	c.mu.Unlock()
	files, err := enumerateImport(ctx, source, storage)
	if err != nil {
		fail(err)
		return
	}
	c.mu.Lock()
	c.importStatus.Total = len(files)
	c.importStatus.Phase = "正在校验并导入"
	c.mu.Unlock()
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		receipt, err := engine.ImportVideo(ctx, path, allowed)
		c.mu.Lock()
		c.importStatus.Checked++
		if err != nil {
			c.importStatus.Failed++
			c.importStatus.Error = path + "：" + err.Error()
		} else if receipt == nil {
			c.importStatus.Skipped++
		} else {
			c.importStatus.Imported++
			c.importReceipts = append(c.importReceipts, *receipt)
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.importStatus.Phase = "导入完成"
	c.mu.Unlock()
}

func (c *Console) cleanupImport(id uint64, confirmed bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !confirmed {
		return errors.New("删除不可逆，必须再次确认")
	}
	if c.closing || c.importStatus.Running || id != c.importStatus.ID || !c.importStatus.CleanupAvailable {
		return errors.New("本次导入不可清理，请刷新状态")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.importCancel = cancel
	c.importDone = make(chan struct{})
	c.importStatus.Running = true
	c.importStatus.CleanupAvailable = false
	c.importStatus.Phase = "正在清理已导入的源文件"
	c.importStatus.Checked = 0
	c.importStatus.Total = len(c.importReceipts)
	c.importStatus.Error = ""
	c.importStatus.Failed = 0
	receipts := append([]cacheproxy.ImportedVideo(nil), c.importReceipts...)
	engine, done := c.service, c.importDone
	go func() {
		defer close(done)
		defer cancel()
		for _, receipt := range receipts {
			err := engine.RemoveImportedSource(ctx, receipt)
			c.mu.Lock()
			c.importStatus.Checked++
			if err == nil {
				c.importStatus.Deleted++
			} else {
				c.importStatus.Failed++
				c.importStatus.Error = receipt.Source + "：" + err.Error()
			}
			c.mu.Unlock()
			if ctx.Err() != nil {
				break
			}
		}
		c.mu.Lock()
		c.importStatus.Running = false
		c.importStatus.Phase = "清理结束"
		c.importCancel = nil
		c.importReceipts = nil
		c.mu.Unlock()
	}()
	return nil
}
