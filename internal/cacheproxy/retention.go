package cacheproxy

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var cacheVideoName = regexp.MustCompile(`^([0-9a-f]{64})\.mp4$`)

type retainedVideo struct {
	path, key    string
	size, recent int64
	score        float64
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
	s.versionPins[v.key]++
	s.retentionMu.Unlock()
}

func (s *Server) releaseVideo(v video) {
	s.retentionMu.Lock()
	s.versionPins[v.key]--
	lastReference := s.versionPins[v.key] == 0
	if lastReference {
		delete(s.versionPins, v.key)
		s.releaseVerified(v.key)
		// Late raw URLs may recreate a previously removed superseded resource.
		var known bool
		if err := s.usage.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM song_videos WHERE version_key=?)`, v.key).Scan(&known); err == nil && known {
			s.cleanupNeeded[v.key] = true
		}
		s.cleanSupersededLocked(v.key)
	}
	s.retentionMu.Unlock()
	// Worker and handler references share one cleanup when the resource becomes idle.
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
	add := func(path, key string) error {
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
		item := retainedVideo{path: path, key: key, size: info.Size(), recent: info.ModTime().UnixMilli()}
		videos = append(videos, item)
		total += item.size
		return nil
	}
	entries, err := os.ReadDir(s.cfg.videosDir())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if match := cacheVideoName.FindStringSubmatch(entry.Name()); match != nil {
			if err := add(filepath.Join(s.cfg.videosDir(), entry.Name()), match[1]); err != nil {
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
		rows, err := s.usage.db.Query(`SELECT resource_key, demand_count, last_demand_at FROM resource_usage`)
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
	}
	for i := range videos {
		item := &videos[i]
		usage := stats[item.key]
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
		if s.versionPins[item.key] > 0 {
			continue
		}
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			s.cfg.Logger.Warn("cache_eviction_failed", "path", item.path, "error", err)
			continue
		}
		total -= item.size
		s.cfg.Logger.Info("cache_evicted", "key", item.key, "path", item.path, "bytes", item.size, "priority", item.score)
	}
	if total > s.cfg.MaxCacheBytes {
		s.cfg.Logger.Info("cache_limit_deferred", "retained_bytes", total, "limit_bytes", s.cfg.MaxCacheBytes)
	}
	return nil
}
