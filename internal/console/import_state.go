package console

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/cacheproxy"
)

type videoImportItem struct {
	File         cacheproxy.ImportFile     `json:"file"`
	Result       string                    `json:"result,omitempty"`
	Reason       string                    `json:"reason,omitempty"`
	Receipt      *cacheproxy.ImportedVideo `json:"receipt,omitempty"`
	Cleanup      string                    `json:"cleanup,omitempty"`
	CleanupError string                    `json:"cleanupError,omitempty"`
}

type videoImportTask struct {
	Version      int               `json:"version"`
	ID           uint64            `json:"id"`
	Source       string            `json:"source"`
	DatabasePath string            `json:"databasePath"`
	VideoDir     string            `json:"videoDir"`
	Enumerated   bool              `json:"enumerated"`
	Files        []videoImportItem `json:"files,omitempty"`
}

func (task videoImportTask) clone() videoImportTask {
	task.Files = append([]videoImportItem(nil), task.Files...)
	return task
}

func (task videoImportTask) status(settings Settings) ImportStatus {
	s := ImportStatus{ID: task.ID, Present: true, Source: task.Source, VideoDir: task.VideoDir, Total: len(task.Files), Paused: !task.Enumerated}
	for _, item := range task.Files {
		switch item.Result {
		case "":
			s.Paused = true
		case "failed":
			s.Checked++
			s.Failed++
			s.Paused = true
			s.Error = item.File.Source + "：" + item.Reason
		case "skipped":
			s.Checked++
			s.Skipped++
			s.Detail = item.File.Source + "：" + item.Reason
		case "imported":
			s.Checked++
			s.Imported++
		}
		if item.Receipt != nil {
			s.receipts++
			s.CleanupAvailable = true
		}
		if item.Cleanup == "deleted" {
			s.Deleted++
		}
		if item.CleanupError != "" {
			s.Error = item.File.Source + "：" + item.CleanupError
		}
	}
	s.ScopeMismatch = cacheproxy.CacheStorageID(cacheproxy.LibraryPaths{DatabasePath: task.DatabasePath, VideoDir: task.VideoDir}) != cacheproxy.CacheStorageID(libraryPaths(settings))
	s.Phase = "导入完成"
	if s.Paused {
		s.Phase = "导入已暂停，等待继续"
	}
	if s.ScopeMismatch {
		s.CleanupAvailable = false
		s.Error = "本次导入的数据位置与当前生效位置不一致，请恢复原位置后重启，或放弃本次导入"
	}
	return s
}

func (c *Console) saveImportTask(task videoImportTask) error {
	if err := writeImportSnapshot(c.files().VideoImport, task); err != nil {
		return importSaveError(err)
	}
	// The SQLite snapshot is now authoritative, even if removing the legacy
	// record fails. Startup always prefers it; no dual writes or replay.
	return importSaveError(removeLegacyImport(c.files().State("video-import")))
}

func importSaveError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("保存导入进度失败，已停止后续处理：%w", err)
}

func removeLegacyImport(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Console) publishImportTask(task videoImportTask) {
	c.mu.Lock()
	settings := c.settings
	c.mu.Unlock()
	copy := task.clone()
	status := task.status(settings)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.importTask = &copy
	status.Running = c.importStatus.Running
	if status.Running {
		status.Pausing = c.importStatus.Pausing
		status.Phase = c.importStatus.Phase
		status.Paused, status.CleanupAvailable = false, false
	}
	c.importStatus = status
}

// Only committed items enter the recovery copy. Publication touches at most a
// bounded batch and never clones or scans the manifest while holding mu.
func (c *Console) publishImportItems(updates []importItemUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &c.importStatus
	for _, update := range updates {
		old := c.importTask.Files[update.index]
		adjustImportCounts(s, old, -1)
		adjustImportCounts(s, update.item, 1)
		c.importTask.Files[update.index] = update.item
		if update.item.Result == "skipped" {
			s.Detail = update.item.File.Source + "：" + update.item.Reason
		}
		if update.item.Result == "failed" {
			s.Error = update.item.File.Source + "：" + update.item.Reason
		}
		if update.item.CleanupError != "" {
			s.Error = update.item.File.Source + "：" + update.item.CleanupError
		}
	}
	s.Paused = !s.Running && (!c.importTask.Enumerated || s.Checked < s.Total || s.Failed > 0)
	s.CleanupAvailable = !s.Running && !s.ScopeMismatch && s.receipts > 0
	if !s.Running {
		s.Phase = "导入完成"
		if s.Paused {
			s.Phase = "导入已暂停，等待继续"
		}
	}
}

func adjustImportCounts(s *ImportStatus, item videoImportItem, delta int) {
	switch item.Result {
	case "imported":
		s.Imported += delta
		s.Checked += delta
	case "skipped":
		s.Skipped += delta
		s.Checked += delta
	case "failed":
		s.Failed += delta
		s.Checked += delta
	}
	if item.Cleanup == "deleted" {
		s.Deleted += delta
	}
	if item.Receipt != nil {
		s.receipts += delta
	}
}

func (c *Console) commitImportTask(task videoImportTask) error {
	if err := c.saveImportTask(task); err != nil {
		return err
	}
	c.publishImportTask(task)
	return nil
}

// Recovery is read-only: no engine, source enumeration or deletion at startup.
func (c *Console) loadImportTask() {
	var task videoImportTask
	_, err := os.Lstat(c.files().VideoImport)
	if os.IsNotExist(err) {
		err = checkImportCompanionsAbsent(c.files().VideoImport)
		if err == nil {
			var data []byte
			data, err = os.ReadFile(c.files().State("video-import"))
			if os.IsNotExist(err) {
				return
			}
			if err == nil {
				err = json.Unmarshal(data, &task)
			}
		}
	} else if err == nil {
		task, err = readImportStore(c.files().VideoImport)
	}
	if err == nil {
		err = task.validate()
	}
	if err != nil {
		c.importStatus = ImportStatus{Present: true, Phase: "导入记录不可用", Error: "无法恢复导入记录，请保留文件排查或放弃本次导入：" + err.Error()}
		return
	}
	c.importTask = &task
	c.importStatus = task.status(c.settings)
}

func (task videoImportTask) validate() error {
	invalid := fmt.Errorf("不支持或不完整的导入记录")
	if task.Version != 1 || task.ID == 0 || !filepath.IsAbs(task.Source) || !filepath.IsAbs(task.DatabasePath) || !filepath.IsAbs(task.VideoDir) || (!task.Enumerated && len(task.Files) != 0) {
		return invalid
	}
	for _, item := range task.Files {
		if !filepath.IsAbs(item.File.Source) || !insideImportRoot(task.Source, item.File.Source) || insideImportRoot(task.VideoDir, item.File.Source) {
			return invalid
		}
		if item.Result != "skipped" && (item.File.FileID == "" || item.File.Size < 0) {
			return invalid
		}
		switch item.Result {
		case "", "failed", "skipped", "imported":
		default:
			return invalid
		}
		switch item.Cleanup {
		case "", "deleted", "missing", "changed":
		default:
			return invalid
		}
		if item.Receipt != nil {
			r := item.Receipt
			key, err := hex.DecodeString(r.MD5)
			if item.Result != "imported" || item.Cleanup != "" || r.ImportFile != item.File || err != nil || len(key) != 16 || filepath.Clean(r.Destination) != filepath.Join(task.VideoDir, r.MD5+".mp4") {
				return invalid
			}
		} else if item.Result == "imported" && item.Cleanup == "" {
			return invalid
		}
	}
	return nil
}
