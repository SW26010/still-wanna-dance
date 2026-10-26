package cacheproxy

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

func validMD5(key string) bool {
	b, err := hex.DecodeString(key)
	return err == nil && len(b) == 16 && key == strings.ToLower(key)
}

type expectedMD5Key struct{}

func WithExpectedMD5(ctx context.Context, md5 string) context.Context {
	return context.WithValue(ctx, expectedMD5Key{}, md5)
}

func (s *Server) RememberSongURL(ctx context.Context, id, target string) (string, error) {
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	v, err := s.parse(r)
	if err != nil {
		return "", err
	}
	if err := s.recordVideo(ctx, v); err != nil {
		return "", err
	}
	return v.key, s.recordSongVideo(ctx, id, v)
}

// ReferenceCheck is a single point-in-time presence snapshot. Missing and
// Present contain only referenced MD5s; orphan files do not affect Complete.
type ReferenceCheck struct {
	Complete   bool
	References map[string]string
	Present    map[string]bool
	Missing    map[string][]string
}

// CheckReferencedFiles does one directory enumeration, never opens video data,
// and checks each distinct MD5 once. Callers consume the result without restat.
func CheckReferencedFiles(ctx context.Context, root string, references map[string]string) (ReferenceCheck, error) {
	r := ReferenceCheck{Complete: true, References: make(map[string]string, len(references)), Present: map[string]bool{}, Missing: map[string][]string{}}
	for id, key := range references {
		if !validMD5(key) {
			return r, fmt.Errorf("invalid MD5 for song %s", id)
		}
		r.References[id] = key
		r.Missing[key] = append(r.Missing[key], id)
	}
	root, err := CacheDirectory(root)
	if err != nil {
		return r, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return r, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		key := strings.TrimSuffix(entry.Name(), ".mp4")
		if entry.Name() != key+".mp4" || !entry.Type().IsRegular() {
			continue
		}
		if _, wanted := r.Missing[key]; wanted {
			r.Present[key] = true
			delete(r.Missing, key)
		}
	}
	for _, ids := range r.Missing {
		sort.Strings(ids)
	}
	r.Complete = len(r.Missing) == 0
	return r, ctx.Err()
}
