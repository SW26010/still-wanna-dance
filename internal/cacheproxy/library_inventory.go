package cacheproxy

import (
	"context"
	"os"
	"strings"
)

type libraryInventory struct {
	files map[string]int64
	bytes int64
}

// Offline readers have no inventory owner and must inspect the selected root.
func scanLibraryInventory(ctx context.Context, paths LibraryPaths) (libraryInventory, error) {
	result := libraryInventory{files: map[string]int64{}}
	files, err := os.ReadDir(paths.VideoDir)
	if err != nil {
		return result, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !cacheVideoName.MatchString(file.Name()) || !file.Type().IsRegular() {
			continue
		}
		info, err := file.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		if info.Mode().IsRegular() {
			result.files[strings.TrimSuffix(file.Name(), ".mp4")] = info.Size()
			result.bytes += info.Size()
		}
	}
	return result, nil
}

// Publication, import, deletion and reconciliation already maintain this stock.
// Copy it under the inventory lock; database and file-identity I/O run unlocked.
func (s *Server) libraryInventory(ctx context.Context, _ LibraryPaths) (libraryInventory, error) {
	s.retentionMu.Lock()
	result := libraryInventory{files: make(map[string]int64, len(s.retained)), bytes: s.retainedBytes}
	for key, item := range s.retained {
		result.files[key] = item.size
	}
	s.retentionMu.Unlock()
	return result, ctx.Err()
}
