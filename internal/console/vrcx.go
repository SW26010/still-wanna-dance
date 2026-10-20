package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrcx"
)

type vrcxPreview struct {
	vrcx.Summary
	ID            string `json:"id"`
	Source        string `json:"source"`
	StorageDir    string `json:"storageDir"`
	Events        int    `json:"events"`
	Duplicates    int    `json:"duplicates"`
	Skipped       int    `json:"skipped"`
	Songs         int    `json:"songs"`
	FirstAt       int64  `json:"firstAt"`
	LastAt        int64  `json:"lastAt"`
	ExistingSongs int    `json:"existingSongs"`
	Imported      bool   `json:"imported"`
	history       *cacheproxy.SongUsageImport
	expires       time.Time
}

var errVRCXWrite = errors.New("历史偏好写入失败或已取消，原统计保持不变；请检查存储空间、权限或占用后重试")

func defaultVRCXPath() string {
	if dir := os.Getenv("APPDATA"); dir != "" {
		return filepath.Join(dir, "VRCX", "VRCX.sqlite3")
	}
	return ""
}

// Called only after the shared Host/Origin/token/terms gate. Detection only
// stats the default file; private history is read solely by explicit preview.
func (c *Console) serveVRCX(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path       string `json:"path"`
		StorageDir string `json:"storageDir"`
		ID         string `json:"id"`
		Overwrite  bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "无效的 VRCX 导入请求", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/api/vrcx/detect" {
		path := defaultVRCXPath()
		info, err := os.Stat(path)
		result := map[string]any{"path": path, "found": err == nil && info.Mode().IsRegular()}
		root, err := c.vrcxTarget(input.StorageDir)
		if err == nil {
			c.lifecycleMu.Lock()
			var count int
			count, err = c.vrcxUsageCount(r.Context(), root)
			c.lifecycleMu.Unlock()
			if err == nil {
				result["existingSongs"] = count
			}
		}
		if err != nil {
			result["targetError"] = "无法读取目标歌曲统计，请检查存储后重试"
		}
		writeJSON(w, result)
		return
	}
	if r.URL.Path == "/api/vrcx/select" {
		path, err := c.selectSetupFolder(r.Context(), "vrcx", input.Path)
		if err != nil {
			http.Error(w, "无法选择数据库，请重试或填写完整路径", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"path": path, "canceled": path == ""})
		return
	}
	// One bounded in-memory preview per Console. Different tabs cannot consume
	// or replace a draft concurrently, and retries never apply it twice.
	if !c.vrcxMu.TryLock() {
		http.Error(w, "VRCX 预览或导入正在进行，请稍后重试", http.StatusConflict)
		return
	}
	defer c.vrcxMu.Unlock()
	if r.URL.Path == "/api/vrcx/discard" {
		if c.vrcxPreview != nil && c.vrcxPreview.ID == input.ID {
			c.vrcxPreview = nil
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if r.URL.Path == "/api/vrcx/preview" {
		c.vrcxPreview = nil
		root, err := c.vrcxTarget(input.StorageDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		path := strings.TrimSpace(input.Path)
		if path == "" {
			path = defaultVRCXPath()
		}
		history := &cacheproxy.SongUsageImport{}
		summary, err := vrcx.Read(ctx, path, history.Observe)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.lifecycleMu.Lock()
		existing, err := c.vrcxUsageCount(ctx, root)
		c.lifecycleMu.Unlock()
		if err != nil {
			http.Error(w, "无法读取目标歌曲统计，请检查存储后重试；未导入", http.StatusConflict)
			return
		}
		p := &vrcxPreview{Summary: summary, Source: path, StorageDir: root,
			Events: history.Events, Duplicates: history.Duplicates, Skipped: summary.Total - history.Events,
			Songs: history.Songs(), FirstAt: history.FirstAt, LastAt: history.LastAt, ExistingSongs: existing,
			history: history, expires: time.Now().Add(15 * time.Minute)}
		if p.Events > 0 {
			var token [24]byte
			if _, err = rand.Read(token[:]); err != nil {
				http.Error(w, "无法创建导入预览，请重试", http.StatusInternalServerError)
				return
			}
			p.ID = hex.EncodeToString(token[:])
			c.vrcxPreview = p
			// Do not retain aggregates after expiration, even without another
			// request. Capture only the token, not the private preview.
			id := p.ID
			time.AfterFunc(15*time.Minute, func() {
				c.vrcxMu.Lock()
				if c.vrcxPreview != nil && c.vrcxPreview.ID == id {
					c.vrcxPreview = nil
				}
				c.vrcxMu.Unlock()
			})
		}
		writeJSON(w, p)
		return
	}
	if r.URL.Path != "/api/vrcx/apply" {
		http.NotFound(w, r)
		return
	}
	p := c.vrcxPreview
	if p == nil || p.ID != input.ID || time.Now().After(p.expires) {
		http.Error(w, "预览已过期或已被替换，请重新预览", http.StatusConflict)
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if err := c.importVRCXPreview(ctx, p, input.Overwrite); err != nil {
		status := http.StatusConflict
		if errors.Is(err, errVRCXWrite) {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, p)
}

func (c *Console) vrcxTarget(selected string) (string, error) {
	c.mu.Lock()
	root := c.settings.StorageDir
	c.mu.Unlock()
	if selected != "" {
		if !filepath.IsAbs(selected) || len(selected) > 4096 || strings.ContainsRune(selected, 0) || c.validateStorageSelection(selected, false) != nil {
			return "", errors.New("请选择有效的目标存储目录")
		}
		if rel, err := filepath.Rel(root, selected); err != nil || rel != "." {
			root = filepath.Clean(selected)
		}
	}
	return root, nil
}

// Caller holds lifecycleMu. Never opens the VRCX source or initializes storage.
func (c *Console) vrcxUsageCount(ctx context.Context, root string) (int, error) {
	c.mu.Lock()
	engine, activeRoot, closing := c.service, c.settings.StorageDir, c.closing
	c.mu.Unlock()
	if closing {
		return 0, errors.New("目标存储状态已改变，请重新检查")
	}
	if engine != nil && root == activeRoot {
		return engine.SongUsageCount(ctx)
	}
	if _, err := c.inspectStorage(root); err != nil {
		return 0, err
	}
	return cacheproxy.ReadSongUsageCount(ctx, root)
}

// Caller holds vrcxMu and lifecycleMu, in that order. Both the settings action
// and the final setup application consume the same checked, in-memory preview.
func (c *Console) importVRCXPreview(ctx context.Context, p *vrcxPreview, overwrite bool) error {
	if time.Now().After(p.expires) {
		return errors.New("预览已过期，请重新检查")
	}
	if p.Imported {
		return nil
	}
	if (p.ExistingSongs > 0 && !overwrite) || (overwrite && p.ExistingSongs == 0) {
		return errors.New("请先预览已有统计并明确确认整表覆盖")
	}
	c.mu.Lock()
	engine, activeRoot, closing := c.service, c.settings.StorageDir, c.closing
	c.mu.Unlock()
	if closing {
		return errors.New("目标存储状态已改变，请重新预览")
	}
	var err error
	if engine != nil && p.StorageDir == activeRoot {
		err = engine.ImportSongUsage(ctx, p.history, overwrite)
	} else {
		if _, err = c.inspectStorage(p.StorageDir); err != nil {
			return err
		}
		err = cacheproxy.ImportSongUsage(ctx, p.StorageDir, p.history, overwrite)
	}
	if err != nil {
		if errors.Is(err, cacheproxy.ErrSongUsageExists) {
			return err
		}
		return errVRCXWrite
	}
	p.Imported = true
	p.history = nil
	return nil
}
