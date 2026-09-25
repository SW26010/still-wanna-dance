package cacheproxy

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var cacheVideoName = regexp.MustCompile(`^[0-9a-f]{64}\.mp4$`)
var songDirectoryName = regexp.MustCompile(`^[1-9][0-9]*$`)

type retainedVideo struct {
	path, id, key string
	size, recent  int64
	score         float64
}

// Recent repeated demand is valuable; a seven-day aging scale prevents old hits
// from dominating forever. HEAD and prefetch never contribute demand_count.
func retentionScore(count, last, now int64) float64 {
	if count <= 0 {
		return 0
	}
	age := math.Max(0, float64(now-last)/float64((7*24*time.Hour).Milliseconds()))
	return math.Log1p(float64(count)) / (1 + age)
}

func (s *Server) pinVideo(v video) {
	s.retentionMu.Lock()
	s.pins[v.id]++
	s.versions[v.key] = v.id
	s.retentionMu.Unlock()
}

func (s *Server) releaseVideo(v video) {
	s.retentionMu.Lock()
	s.pins[v.id]--
	lastReference := s.pins[v.id] == 0
	if lastReference {
		delete(s.pins, v.id)
	}
	s.retentionMu.Unlock()
	// Worker and handler references share one cleanup when the song becomes idle.
	if lastReference {
		s.trimCache()
	}
}

// Pins span both background workers and response lifetimes. Serializing scans
// with pin acquisition closes the validation/open/delete race on Windows too.
func (s *Server) trimCache() {
	if s.cfg.MaxCacheBytes == 0 {
		return
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if err := s.trimCacheLocked(); err != nil {
		s.cfg.Logger.Warn("cache_eviction_failed", "error", err)
	}
}

func (s *Server) trimCacheLocked() error {
	var videos []retainedVideo
	var total int64
	add := func(path, id, key string) error {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		item := retainedVideo{path: path, id: id, key: key, size: info.Size(), recent: info.ModTime().UnixMilli()}
		videos = append(videos, item)
		total += item.size
		return nil
	}
	entries, err := os.ReadDir(s.cfg.SongsDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && songDirectoryName.MatchString(entry.Name()) {
			if err := add(filepath.Join(s.cfg.SongsDir, entry.Name(), "video.mp4"), entry.Name(), ""); err != nil {
				return err
			}
		}
	}
	entries, err = os.ReadDir(s.cfg.CacheDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if cacheVideoName.MatchString(entry.Name()) {
			key := strings.TrimSuffix(entry.Name(), ".mp4")
			if err := add(filepath.Join(s.cfg.CacheDir, entry.Name()), s.versions[key], key); err != nil {
				return err
			}
		}
	}
	if total <= s.cfg.MaxCacheBytes {
		return nil
	}
	// Only over-limit caches need usage synchronization, database reads and ranking.
	s.usage.flush()
	now := time.Now().UnixMilli()
	stats := map[string]retainedVideo{}
	if s.usage != nil {
		rows, err := s.usage.db.Query(`SELECT song_id, demand_count, last_demand_at FROM song_usage`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var count, last int64
			if err := rows.Scan(&id, &count, &last); err != nil {
				rows.Close()
				return err
			}
			stats[id] = retainedVideo{score: retentionScore(count, last, now), recent: last}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		// Load historical mappings once; pinVideo tracks every new version.
		if !s.versionsLoaded {
			rows, err = s.usage.db.Query(`SELECT DISTINCT version_key, song_id FROM request_events`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var key, id string
				if err := rows.Scan(&key, &id); err != nil {
					rows.Close()
					return err
				}
				s.versions[key] = id
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			s.versionsLoaded = true
		}
	}
	for i := range videos {
		item := &videos[i]
		if item.key != "" {
			item.id = s.versions[item.key]
		}
		usage := stats[item.id]
		item.score = usage.score
		if usage.recent != 0 {
			item.recent = usage.recent
		}
	}
	sort.Slice(videos, func(i, j int) bool {
		a, b := videos[i], videos[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if a.recent != b.recent {
			return a.recent < b.recent
		}
		// Prefer dropping the redundant cache copy on ties.
		if (a.key != "") != (b.key != "") {
			return a.key != ""
		}
		return a.path < b.path
	})
	planned := total
	for _, item := range videos {
		if planned <= s.cfg.MaxCacheBytes {
			break
		}
		// A protected low-priority video is a deferred victim, not a reason
		// to evict a more valuable video while a prefetch is still finishing.
		planned -= item.size
		if s.pins[item.id] > 0 {
			continue
		}
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			s.cfg.Logger.Warn("cache_eviction_failed", "path", item.path, "error", err)
			continue
		}
		total -= item.size
		s.cfg.Logger.Info("cache_evicted", "song_id", item.id, "path", item.path, "bytes", item.size, "priority", item.score)
	}
	if total > s.cfg.MaxCacheBytes {
		s.cfg.Logger.Info("cache_limit_deferred", "retained_bytes", total, "limit_bytes", s.cfg.MaxCacheBytes)
	}
	return nil
}
