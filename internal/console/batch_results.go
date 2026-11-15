package console

import (
	"net/http"
	"slices"
	"strconv"
)

const batchFailurePageSize = 50

func (c *Console) serveBatchFailures(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	offset := 0
	var err error
	if raw := values.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
	}
	if err != nil || offset < 0 || values.Get("id") == "" {
		preserveResponseFault(w, rejectRequest(err))
		http.Error(w, "无效查询", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	if values.Get("id") != c.batch.ID {
		c.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]string{"error": "批量任务已变化，请刷新后查看", "code": "batch_changed"})
		return
	}
	total := len(c.batch.Failures)
	start := min(offset, total)
	// Bound the copy and lock time by the page size, even during a running fill.
	failures := slices.Clone(c.batch.Failures[start : start+min(batchFailurePageSize, total-start)])
	c.mu.Unlock()
	writeJSON(w, struct {
		Offset   int       `json:"offset"`
		Total    int       `json:"total"`
		Failures []Failure `json:"failures"`
	}{offset, total, failures})
}
