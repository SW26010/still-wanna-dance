package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) songPolicyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result any
	var err error
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		offset := 0
		if q.Get("offset") != "" {
			offset, err = strconv.Atoi(q.Get("offset"))
		}
		policy := cacheproxy.SongPolicy(q.Get("policy"))
		query := q.Get("q")
		if err != nil || offset < 0 || offset > 10000000 || policy != "" && !policy.Valid() || !utf8.ValidString(query) || len(utf16.Encode([]rune(query))) > 300 {
			http.Error(w, "无效查询", http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		root := libraryPaths(c.settings)
		c.mu.Unlock()
		result, err = cacheproxy.ReadSongPage(ctx, root, query, policy, offset)
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		var input struct {
			StorageID string                `json:"storageID"`
			ID        string                `json:"id"`
			Policy    cacheproxy.SongPolicy `json:"policy"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input)
		id, idErr := strconv.ParseInt(input.ID, 10, 64)
		if err == nil && (idErr != nil || id <= 0 || !input.Policy.Valid()) {
			err = errors.New("无效歌曲 ID 或偏好")
		}
		if err == nil {
			c.lifecycleMu.Lock()
			defer c.lifecycleMu.Unlock()
			c.mu.Lock()
			if c.closing {
				err = errors.New("程序正在退出")
			} else if input.StorageID != cacheproxy.DatabaseID(libraryPaths(c.settings).DatabasePath) {
				err = errors.New("存储目录已切换，请刷新歌曲列表")
			}
			store, storeErr := c.businessStore()
			c.mu.Unlock()
			if err == nil {
				if storeErr != nil {
					err = storeErr
				} else {
					err = store.SetSongPolicy(ctx, id, input.Policy)
				}
			}
		}
		result = map[string]bool{"ok": err == nil}
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, result)
}
