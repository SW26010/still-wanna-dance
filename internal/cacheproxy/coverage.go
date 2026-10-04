package cacheproxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// CachedChecksums lists immutable MD5-named files, including unregistered content.
func CachedChecksums(ctx context.Context, root string) (map[string]bool, error) {
	entries, err := os.ReadDir(filepath.Join(root, "videos"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	result := map[string]bool{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := strings.TrimSuffix(e.Name(), ".mp4")
		if e.Type().IsRegular() && e.Name() == key+".mp4" && validMD5(key) {
			result[key] = true
		}
	}
	return result, nil
}
