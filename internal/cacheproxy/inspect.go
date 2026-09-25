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
func CheckLocal(ctx context.Context, storageDir, target string) (bool, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	parser := &Server{cfg: DefaultConfig()}
	v, err := parser.parse(r)
	if err != nil {
		return false, err
	}
	err = checkFile(ctx, filepath.Join(storageDir, "videos", v.id+"-"+v.key+".mp4"), v)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errInvalidCache) {
		return false, nil
	}
	return err == nil, err
}
