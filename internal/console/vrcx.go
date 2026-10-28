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
	"still-wanna-dance/internal/workfiles"
)

type vrcxPreview struct {
	vrcx.Summary
	ID            string `json:"id"`
	Source        string `json:"source"`
	DatabasePath  string `json:"databasePath"`
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
// stats the default file; private history is read solely after a user action.
func (c *Console) serveVRCX(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path      string `json:"path"`
		ID        string `json:"id"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "无效的 VRCX 导入请求", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/api/vrcx/detect" {
		path := defaultVRCXPath()
		info, err := os.Stat(path)
		result := map[string]any{"path": path, "found": err == nil && info.Mode().IsRegular()}
		c.lifecycleMu.Lock()
		count, err := c.vrcxUsageCount(r.Context(), c.vrcxTarget())
		c.lifecycleMu.Unlock()
		if err != nil {
			result["targetError"] = "无法读取目标歌曲统计，请检查存储后重试"
		} else {
			result["existingSongs"] = count
			c.mu.Lock()
			suggested := !c.vrcxImportDismissed && count == 0 && result["found"] == true
			result["suggested"] = suggested
			result["notify"] = suggested && c.vrcxNotify
			c.mu.Unlock()
		}
		writeJSON(w, result)
		return
	}
	if r.URL.Path == "/api/vrcx/dismiss" {
		if err := c.dismissVRCXSuggestion(); err != nil {
			http.Error(w, "无法保存忽略选择，请重试", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
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
		root := c.vrcxTarget()
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
		p := &vrcxPreview{Summary: summary, Source: path, DatabasePath: root,
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

// The choice belongs to this configuration instance, not to a video directory.
// Ordinary settings writes preserve this non-editable field in the disk envelope.
func (c *Console) dismissVRCXSuggestion() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	closing, dismissed := c.closing, c.vrcxImportDismissed
	c.mu.Unlock()
	if closing {
		return context.Canceled
	}
	if dismissed {
		return nil
	}
	_, fields, err := readConfigFile(c.configPath)
	if err != nil {
		return err
	}
	if fields == nil {
		return os.ErrNotExist
	}
	fields["vrcxImportDismissed"] = json.RawMessage("true")
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	if err := workfiles.Write(c.configPath, data); err != nil {
		return err
	}
	c.mu.Lock()
	c.vrcxImportDismissed = true
	c.mu.Unlock()
	return nil
}

func (c *Console) vrcxTarget() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return libraryPaths(c.settings).DatabasePath
}

// Caller holds lifecycleMu. Never opens the VRCX source or initializes storage.
func (c *Console) vrcxUsageCount(ctx context.Context, databasePath string) (int, error) {
	c.mu.Lock()
	closing, active := c.closing, libraryPaths(c.settings).DatabasePath
	c.mu.Unlock()
	if closing || databasePath != active {
		return 0, errors.New("目标数据库已改变，请重新检查")
	}
	store, err := c.businessStore()
	if err != nil {
		return 0, err
	}
	return store.SongUsageCount(ctx)
}

// Caller holds vrcxMu and lifecycleMu, in that order. Import consumes the checked in-memory preview.
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
	activeRoot, closing := libraryPaths(c.settings).DatabasePath, c.closing
	c.mu.Unlock()
	if closing || p.DatabasePath != activeRoot {
		return errors.New("目标数据库已改变，请重新预览")
	}
	store, err := c.businessStore()
	if err != nil {
		return err
	}
	err = store.ImportSongUsage(ctx, p.history, overwrite)
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
