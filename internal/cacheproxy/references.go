package cacheproxy

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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
	entries, err := os.ReadDir(filepath.Join(root, "videos"))
	if err != nil && !os.IsNotExist(err) {
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

func (s *Server) refreshCatalogProtection(songs []CatalogSong) {
	s.retentionMu.Lock()
	now := time.Now()
	for _, song := range songs {
		id, key := strconv.FormatInt(song.ID, 10), song.MD5
		// Catalog authority changes the persistent mapping, not reservations
		// for content already prefetched for a pending song or its handoff.
		if s.queueSongs[id] || now.Before(s.queueHandoffs[id]) {
			s.rememberSongResourceLocked(id, key)
			continue
		}
		if s.songResources == nil {
			s.songResources = make(map[string]map[string]bool)
		}
		s.songResources[id] = map[string]bool{key: true}
	}
	s.queueProtected = map[string]bool{}
	for id := range s.queueSongs {
		for key := range s.songResources[id] {
			s.queueProtected[key] = true
		}
	}
	s.refreshHandoffsLocked(now)
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
}

// CheckReferences owns the database snapshot and pins its resources until the
// caller finishes using it. Internal eviction cannot invalidate presence results.
// No snapshot is cached across operations or catalog refreshes.
func (s *Server) CheckReferences(ctx context.Context) (ReferenceCheck, func(), error) {
	if !s.beginRequest() {
		return ReferenceCheck{}, func() {}, context.Canceled
	}
	s.mappingMu.Lock()
	rows, err := s.usage.db.QueryContext(ctx, "SELECT song_id, md5 FROM song_media")
	refs := map[string]string{}
	if err == nil {
		for rows.Next() {
			var id, key string
			if err = rows.Scan(&id, &key); err != nil {
				break
			}
			refs[id] = key
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	keys := map[string]bool{}
	if err == nil {
		for _, key := range refs {
			if !keys[key] {
				s.pinVideo(video{key: key})
				keys[key] = true
			}
		}
	}
	s.mappingMu.Unlock()
	release := func() {
		for key := range keys {
			s.releaseVideo(video{key: key})
		}
		s.wg.Done()
	}
	if err != nil {
		release()
		return ReferenceCheck{}, func() {}, err
	}
	result, err := CheckReferencedFiles(ctx, s.cfg.StorageDir, refs)
	if err != nil {
		release()
		return result, func() {}, err
	}
	return result, release, nil
}
