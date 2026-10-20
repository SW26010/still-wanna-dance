package cacheproxy

import (
	"context"
	"os"
	"path/filepath"
)

type SongEntry struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Policy SongPolicy `json:"policy"`
	Saved  bool       `json:"saved"`
}

type SongPage struct {
	StorageID string      `json:"storageID"`
	Entries   []SongEntry `json:"entries"`
	Total     int         `json:"total"`
}

// ReadSongPage reads local metadata and file existence only. Looking at the
// catalog, including favorites with missing files, must not start an engine.
func ReadSongPage(ctx context.Context, root, query string, policy SongPolicy, offset int) (SongPage, error) {
	root, err := filepath.Abs(root)
	result := SongPage{StorageID: CacheStorageID(root), Entries: []SongEntry{}}
	if err != nil {
		return result, err
	}
	db, err := readStorageDatabase(root)
	if err != nil || db == nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	const from = ` FROM songs s LEFT JOIN song_media m ON m.song_id=s.song_id
 LEFT JOIN song_preferences p ON p.song_id=s.song_id
 WHERE (?='' OR instr(lower(COALESCE(s.name,s.composed_title,'')),lower(?))>0 OR CAST(s.song_id AS TEXT)=?)
 AND (?='' OR COALESCE(p.policy,'normal')=?)`
	args := []any{query, query, query, policy, policy}
	if err := tx.QueryRowContext(ctx, "SELECT count(*)"+from, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT CAST(s.song_id AS TEXT), substr(COALESCE(s.name,s.composed_title,''),1,300), COALESCE(p.policy,'normal'), COALESCE(m.md5,'')"+from+" ORDER BY s.song_id DESC LIMIT 50 OFFSET ?", append(args, offset)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry SongEntry
		var key string
		if err := rows.Scan(&entry.ID, &entry.Title, &entry.Policy, &key); err != nil {
			return result, err
		}
		if key != "" {
			info, err := os.Lstat(filepath.Join(root, "videos", key+".mp4"))
			if err != nil && !os.IsNotExist(err) {
				return result, err
			}
			entry.Saved = err == nil && info.Mode().IsRegular()
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, rows.Err()
}
