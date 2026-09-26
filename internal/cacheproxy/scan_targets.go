package cacheproxy

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ScanTarget struct{ Checksum, Target string }

// LoadScanTargets reads existing song associations without starting an engine,
// updating versions or creating a missing database. URLs are for local checks;
// a later download must resolve a fresh address if its local receipt is lost.
func LoadScanTargets(ctx context.Context, root string) (map[string]ScanTarget, error) {
	path, err := filepath.Abs(filepath.Join(root, "stepstash.sqlite"))
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT c.song_id, v.version_key, v.checksum, v.file_bytes, v.source_path FROM current_videos c JOIN video_versions v USING(version_key)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ScanTarget{}
	for rows.Next() {
		var id, key, checksum, path string
		var size int64
		if err := rows.Scan(&id, &key, &checksum, &size, &path); err != nil {
			return nil, err
		}
		target := (&url.URL{Scheme: "https", Host: "nya.xin.moe", Path: path, RawQuery: url.Values{"e": {checksum}, "s": {strconv.FormatInt(size, 10)}}.Encode()}).String()
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			continue
		}
		v, err := parseVideo(req, DefaultConfig().MaxFileBytes)
		if err == nil && v.key == key {
			result[id] = ScanTarget{Checksum: v.checksum, Target: target}
		}
	}
	return result, rows.Err()
}
