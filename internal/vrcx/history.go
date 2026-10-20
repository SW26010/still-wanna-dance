// Package vrcx reads a user-selected history snapshot without storing private
// URLs, names, users or locations. It never opens the source for writing.
package vrcx

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const maxRows = 2_000_000

type Summary struct {
	Total      int `json:"total"`
	Recognized int `json:"recognized"`
	Invalid    int `json:"invalid"`
	Unrelated  int `json:"unrelated"`
}

type event struct{ id, at int64 }

// Read uses one SQLite SELECT snapshot, including committed WAL records. No
// immutable/nolock mode, checkpoint, journal-mode change or main-file copy.
// observe is called in time order only after the entire read succeeds.
func Read(ctx context.Context, path string, observe func(id, at int64)) (Summary, error) {
	return read(ctx, path, time.Now(), maxRows, observe)
}

func read(ctx context.Context, path string, now time.Time, limit int, observe func(id, at int64)) (Summary, error) {
	var summary Summary
	if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return summary, errors.New("请选择 VRCX SQLite 数据库的完整路径")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return summary, errors.New("找不到或无法读取所选 VRCX 数据库，请检查路径和读取权限")
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(1000)", "trusted_schema(0)"}}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return summary, readError(ctx, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// Require a real table, not a user-defined view. Bound field reads as well
	// as row count; a malformed source must not allocate arbitrarily large URLs.
	var table string
	if err = db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='gamelog_video_play'`).Scan(&table); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return summary, errors.New("所选数据库缺少 VRCX 播放记录表 gamelog_video_play")
		}
		return summary, readError(ctx, err)
	}
	rows, err := db.QueryContext(ctx, `SELECT
		CASE WHEN typeof(created_at)='text' AND length(created_at)<=64 THEN created_at END,
		CASE WHEN typeof(video_url)='text' AND length(video_url)<=8192 THEN video_url END
		FROM gamelog_video_play LIMIT ?`, limit+1)
	if err != nil {
		return summary, readError(ctx, err)
	}
	defer rows.Close()
	var events []event
	for rows.Next() {
		if ctx.Err() != nil {
			return summary, readError(ctx, ctx.Err())
		}
		summary.Total++
		if summary.Total > limit {
			return summary, errors.New("VRCX 播放记录超过 200 万条，未生成预览；请选用较小的完整历史数据库")
		}
		var created, target sql.NullString
		if err = rows.Scan(&created, &target); err != nil {
			return summary, readError(ctx, err)
		}
		id, related := songID(target.String)
		if !target.Valid || (related && id == 0) {
			summary.Invalid++
			continue
		}
		if !related {
			summary.Unrelated++
			continue
		}
		summary.Recognized++
		at, err := time.Parse(time.RFC3339Nano, created.String)
		if err != nil || !created.Valid || at.UnixMilli() <= 0 || at.After(now) {
			summary.Invalid++
			continue
		}
		events = append(events, event{id, at.UnixMilli()})
	}
	if err = rows.Err(); err != nil {
		return summary, readError(ctx, err)
	}
	if err = rows.Close(); err != nil {
		return summary, readError(ctx, err)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at < events[j].at })
	for _, event := range events {
		if ctx.Err() != nil {
			return summary, readError(ctx, ctx.Err())
		}
		observe(event.id, event.at)
	}
	return summary, nil
}

// Known host/path only. video_id, names, nodes and transport variants do not
// determine identity. Ambiguous IDs and non-default ports are not accepted.
func songID(raw string) (int64, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		!strings.EqualFold(u.Hostname(), "api.udon.dance") || u.Path != "/Api/Songs/play" {
		return 0, false
	}
	if u.User != nil || (u.Port() != "" && !((u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443"))) {
		return 0, true
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["id"]) != 1 {
		return 0, true
	}
	value := query.Get("id")
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, true
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, true
	}
	return id, true
}

// SQLite errors can contain file paths or schema text. Return only fixed
// diagnostics, including for a corrupt or maliciously formed source.
func readError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New("VRCX 读取已取消或超时；未导入，请稍后重试")
	}
	if strings.Contains(err.Error(), "no such column") {
		return errors.New("所选 VRCX 数据库缺少 created_at 或 video_url 字段")
	}
	return errors.New("无法安全读取 VRCX 数据库；请检查格式、读取权限或数据库占用后重试")
}
