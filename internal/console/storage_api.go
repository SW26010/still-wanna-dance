package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *settingsEditor) serveStorage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DatabasePath  string  `json:"databasePath"`
		VideoDir      string  `json:"videoDir"`
		BlockCacheDir *string `json:"blockCacheDir"`
		Selection     string  `json:"selection"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decodeRequest(decoder, &input); err != nil {
		preserveResponseFault(w, err)
		http.Error(w, "无效的数据位置请求", 400)
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	s, old, closing := c.savedSettings, c.savedSettings, c.closing
	active := c.settings
	c.mu.Unlock()
	if closing {
		http.Error(w, "程序正在退出", 409)
		return
	}
	s.DatabasePath, s.VideoDir = input.DatabasePath, input.VideoDir
	if input.BlockCacheDir != nil {
		s.BlockCacheDir = *input.BlockCacheDir
	}
	s, err := c.resolveResourceSettings(resolveConfigPaths(s, c.configPath))
	if r.URL.Path == "/api/storage/validate-location" {
		choice, field := input.Selection, ""
		if choice != "database" && choice != "video" && choice != "cache" {
			http.Error(w, "无效的存储位置选择", http.StatusBadRequest)
			return
		}
		if err == nil && storageChoiceChanged(s, old, choice) {
			field = choice
			err = c.validateStorageChoice(s, choice)
		}
		if err == nil {
			field, err = c.firstInvalidChangedStorageChoice(s, old)
		}
		if err != nil {
			writeJSON(w, map[string]any{"valid": false, "field": field, "message": err.Error()})
		} else {
			writeJSON(w, map[string]bool{"valid": true})
		}
		return
	}
	if err == nil {
		switch r.URL.Path {
		case "/api/storage/check":
			var confirmation bool
			if err = c.validateChangedStorageChoices(s, old); err == nil {
				confirmation, err = c.inspectLibrarySelection(s, old)
			}
			if err == nil {
				writeJSON(w, map[string]bool{"confirmVideoPolicy": confirmation})
				return
			}
		case "/api/storage/create-database", "/api/storage/prepare-database":
			if strings.TrimSpace(s.DatabasePath) == "" {
				err = errors.New("请选择要新建的数据库文件位置")
				break
			}
			// Re-selecting an existing saved database keeps legacy locations usable;
			// its file is still inspected below, and new choices remain restricted.
			if r.URL.Path == "/api/storage/create-database" || storageChoiceChanged(s, old, "database") {
				if err = c.validateStorageChoice(s, "database"); err != nil {
					break
				}
			}
			if err = c.validateChangedStorageChoices(s, old); err != nil {
				break
			}
			if r.URL.Path == "/api/storage/prepare-database" {
				if _, statErr := os.Lstat(s.DatabasePath); statErr == nil {
					// Existing files are inspected read-only, never recreated or migrated.
					if err = cacheproxy.CheckDatabaseSelection(s.DatabasePath); err == nil {
						writeJSON(w, map[string]any{"path": s.DatabasePath, "created": false})
						return
					}
					break
				} else if !os.IsNotExist(statErr) {
					err = statErr
					break
				}
				if sameResourcePath(s.DatabasePath, old.DatabasePath) || sameResourcePath(s.DatabasePath, active.DatabasePath) {
					err = errors.New("已配置的数据库文件丢失，请恢复原文件，或选择新的文件名新建数据库")
					break
				}
			}
			for _, current := range []Settings{active, old} {
				if err = c.validateStorageChoice(Settings{DatabasePath: s.DatabasePath, VideoDir: current.VideoDir, BlockCacheDir: current.BlockCacheDir}, "database"); err != nil {
					err = fmt.Errorf("新建数据库前，请先避开已保存或正在使用的位置：%w", err)
					break
				}
			}
			if err != nil {
				break
			}
			var store *cacheproxy.Store
			store, err = cacheproxy.CreateStore(s.DatabasePath)
			if err == nil {
				err = store.Close()
			}
			if err == nil {
				writeJSON(w, map[string]any{"path": s.DatabasePath, "created": true})
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
	}
	preserveResponseFault(w, err)
	if writeSettingsValidation(w, err) {
		return
	}
	http.Error(w, err.Error(), 400)
}
