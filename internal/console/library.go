package console

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"still-wanna-dance/internal/cacheproxy"
)

type libraryPage struct {
	cacheproxy.LibraryPage
	Covers map[string]songCoverChoice `json:"covers,omitempty"`
}

func (c *Console) libraryAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	values := r.URL.Query()
	q := cacheproxy.LibraryQuery{View: values.Get("view"), Search: values.Get("q"), Sort: values.Get("sort"), Direction: values.Get("direction"), Filter: values.Get("filter")}
	// Legacy object links become ordinary field searches, never extra constraints.
	song, resource := values.Get("song"), values.Get("resource")
	if song != "" || resource != "" {
		if q.Search != "" || song != "" && resource != "" {
			preserveResponseFault(w, rejectRequest(nil))
			http.Error(w, "请只使用一个搜索条件", 400)
			return
		}
		if song != "" {
			q.Search = "id:" + song
		} else {
			q.Search = "md5:" + resource
		}
	}
	var err error
	if raw := values.Get("offset"); raw != "" {
		q.Offset, err = strconv.Atoi(raw)
	}
	valid := err == nil && q.Offset >= 0 && q.Offset <= 10000000 && utf8.ValidString(q.Search) && len(utf16.Encode([]rune(q.Search))) <= 300
	valid = valid && (q.View == "" || q.View == "songs" || q.View == "resources") && (q.Sort == "" || q.Sort == "id" || q.Sort == "title" || q.Sort == "score" || q.Sort == "size" || q.Sort == "recent" || q.Sort == "resource" && q.View == "resources")
	valid = valid && (q.Direction == "" || q.Direction == "ascending" || q.Direction == "descending")
	valid = valid && (q.Filter == "" || q.Filter == "saved" || q.Filter == "missing" || q.View != "resources" && (q.Filter == "normal" || q.Filter == "favorite" || q.Filter == "blocked"))
	valid = valid && (values.Get("covers") == "" || values.Get("covers") == "1" && q.View != "resources")
	if !valid {
		preserveResponseFault(w, rejectRequest(err))
		http.Error(w, "无效查询", 400)
		return
	}
	if _, err := cacheproxy.ParseLibrarySearch(q.Search); err != nil {
		preserveResponseFault(w, rejectRequest(err))
		w.WriteHeader(400)
		writeJSON(w, map[string]string{"error": err.Error(), "code": "invalid_library_search"})
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
	// Preserve startup's pending-selection result instead of attempting to
	// read an unselected database and reporting it as a new runtime fault.
	var page cacheproxy.LibraryPage
	_, err = c.businessStore()
	if err == nil {
		page, err = cacheproxy.ReadLibraryPage(ctx, root, q)
	}
	if err != nil {
		preserveResponseFault(w, err)
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
	result := libraryPage{LibraryPage: page}
	if values.Get("covers") == "1" {
		ids := make([]string, 0, len(page.Songs))
		for _, song := range page.Songs {
			ids = append(ids, song.ID)
		}
		result.Covers = c.covers.choices(filepath.Join(c.files().Root, "data", "covers"), ids)
	}
	writeJSON(w, result)
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
