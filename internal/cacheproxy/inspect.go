package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

// CheckLocal validates existing bytes only. It never starts an engine, downloads,
// publishes, deletes, or updates usage. A missing/corrupt video is a scan result;
// an unreadable file is an error and must not replace a successful snapshot.
func CheckLocal(ctx context.Context, songsDir, cacheDir, target string) (bool, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	parser := &Server{cfg: DefaultConfig()}
	v, err := parser.parse(r)
	if err != nil {
		return false, err
	}
	var readErr error
	for _, path := range []string{filepath.Join(songsDir, v.id, "video.mp4"), filepath.Join(cacheDir, v.key+".mp4")} {
		err := checkFile(ctx, path, v)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errInvalidCache) {
			readErr = err
		}
	}
	return false, readErr
}
