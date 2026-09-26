package cacheproxy

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// CachedChecksums inventories published, registered files by content, so one
// video can cover several songs. This checks presence/length, not a fresh hash.
func CachedChecksums(ctx context.Context, root string) (map[string]bool, error) {
	db, err := openScanDatabase(root)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT version_key, checksum, file_bytes FROM video_versions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var key, checksum string
		var size int64
		if err := rows.Scan(&key, &checksum, &size); err != nil {
			return nil, err
		}
		k, ke := hex.DecodeString(key)
		digest, de := hex.DecodeString(checksum)
		if ke != nil || len(k) != 32 || de != nil || len(digest) != 16 || size <= 0 {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, "videos", key+".mp4"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() && info.Size() == size {
			result[strings.ToLower(checksum)] = true
		}
	}
	return result, rows.Err()
}
