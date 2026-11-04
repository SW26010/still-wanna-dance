package console

import (
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
)

func (c *Console) serveLogs(w http.ResponseWriter, r *http.Request) {
	query := applog.Query{Level: r.URL.Query().Get("level"), Event: r.URL.Query().Get("event")}
	for _, field := range []struct {
		name  string
		value *time.Time
	}{{"from", &query.From}, {"to", &query.To}} {
		if raw := r.URL.Query().Get(field.name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				preserveResponseFault(w, rejectRequest(err))
				http.Error(w, "日志时间范围无效", http.StatusBadRequest)
				return
			}
			*field.value = time.UnixMilli(value)
		}
	}
	if err := query.Validate(); err != nil {
		preserveResponseFault(w, rejectRequest(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/api/logs/export" {
		result, err := applog.Export(r.Context(), c.files().LogDir, query)
		if err != nil {
			preserveResponseFault(w, err)
			http.Error(w, "日志导出失败，请重新尝试；若仍失败，请检查日志文件和目录权限", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="swd-logs.txt"`)
		w.Header().Set("X-SWD-Log-Matches", strconv.Itoa(result.Matches))
		w.Header().Set("X-SWD-Log-Skipped", strconv.Itoa(result.Skipped))
		_, _ = w.Write(result.Data)
		return
	}
	result, err := applog.Read(r.Context(), c.files().LogDir, query)
	if err != nil {
		preserveResponseFault(w, err)
		http.Error(w, "程序日志读取失败，请重新查询；若仍失败，请检查日志文件和目录权限", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
