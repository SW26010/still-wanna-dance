package console

import (
	"context"
	"net/http"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) libraryAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	values := r.URL.Query()
	q := cacheproxy.LibraryQuery{View: values.Get("view"), Search: values.Get("q"), Sort: values.Get("sort"), Filter: values.Get("filter"), SongID: values.Get("song"), Resource: values.Get("resource")}
	var err error
	if raw := values.Get("offset"); raw != "" {
		q.Offset, err = strconv.Atoi(raw)
	}
	valid := err == nil && q.Offset >= 0 && q.Offset <= 10000000 && utf8.ValidString(q.Search) && len(utf16.Encode([]rune(q.Search))) <= 300
	valid = valid && (q.View == "" || q.View == "songs" || q.View == "resources") && (q.Sort == "" || q.Sort == "id" || q.Sort == "title" || q.Sort == "score" || q.Sort == "size" || q.Sort == "recent")
	valid = valid && (q.Filter == "" || q.Filter == "saved" || q.Filter == "missing" || q.View != "resources" && (q.Filter == "normal" || q.Filter == "favorite" || q.Filter == "blocked"))
	if q.SongID != "" {
		id, e := strconv.ParseInt(q.SongID, 10, 64)
		valid = valid && e == nil && id > 0 && strconv.FormatInt(id, 10) == q.SongID && q.View != "resources"
	}
	if q.Resource != "" {
		valid = valid && validLibraryResource(q.Resource)
	}
	if !valid {
		http.Error(w, "无效查询", 400)
		return
	}
	c.mu.Lock()
	root, closing := libraryPaths(c.settings), c.closing
	c.mu.Unlock()
	if closing {
		http.Error(w, "程序正在退出", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	page, err := cacheproxy.ReadLibraryPage(ctx, root, q)
	if err != nil {
		w.WriteHeader(400)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	c.mu.Lock()
	service, scanning, changed := c.service, c.batch.Running && c.batch.ScanOnly, c.closing || root != libraryPaths(c.settings)
	c.mu.Unlock()
	if changed {
		http.Error(w, "程序实例或存储目录已变化，请刷新", 409)
		return
	}
	var local []*cacheproxy.CacheEntry
	for _, resource := range page.Resources {
		if resource.Local != nil {
			local = append(local, resource.Local)
		}
	}
	if service != nil {
		service.MarkCacheProtection(local)
	}
	for _, entry := range local {
		entry.Protected = entry.Protected || scanning
	}
	writeJSON(w, page)
}

func validLibraryResource(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, c := range key {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
