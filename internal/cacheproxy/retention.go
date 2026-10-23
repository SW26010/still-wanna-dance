package cacheproxy

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var cacheVideoName = regexp.MustCompile(`^([0-9a-f]{32})\.mp4$`)

type retainedVideo struct {
	path, key    string
	size, recent int64
	modified     int64
	songID       int64
	score        float64
}

// demandHalfLife applies independently to every counted request.
const demandHalfLife = 60 * 24 * time.Hour

// retentionScore evaluates an accumulator stored at last, without mutating it.
func retentionScore(score float64, last, now int64) float64 {
	if score <= 0 {
		return 0
	}
	age := math.Max(0, float64(now-last)/float64(demandHalfLife.Milliseconds()))
	return score * math.Exp2(-age)
}

func (s *Server) pinVideo(v video) {
	s.retentionMu.Lock()
	for s.deletingVideos[v.key] != nil {
		done := s.deletingVideos[v.key]
		s.retentionMu.Unlock()
		<-done
		s.retentionMu.Lock()
	}
	s.versionPins[v.key]++
	s.retentionMu.Unlock()
}

func (s *Server) releaseVideo(v video) {
	var file *os.File
	s.retentionMu.Lock()
	s.versionPins[v.key]--
	lastReference := s.versionPins[v.key] == 0
	if lastReference {
		delete(s.versionPins, v.key)
		file = s.detachVerified(v.key)
		if file != nil {
			// Keep cleanup and new pins away until Windows releases the handle.
			s.beginVideoRemovalLocked(v.key)
		}
	}
	s.retentionMu.Unlock()
	if file != nil {
		file.Close()
		s.finishVideoRemoval(v.key, false)
	}
	if lastReference {
		s.retentionMu.Lock()
		s.requestRetentionLocked()
		s.retentionMu.Unlock()
	}
}

// Reserve deletion while holding retentionMu, then perform disk I/O unlocked.
// New pins wait only for this key; existing pins prevent the reservation.
func (s *Server) beginVideoRemovalLocked(key string) bool {
	if s.versionPins[key] > 0 || s.deletingVideos[key] != nil {
		return false
	}
	if s.deletingVideos == nil {
		s.deletingVideos = make(map[string]chan struct{})
	}
	s.deletingVideos[key] = make(chan struct{})
	return true
}

func (s *Server) finishVideoRemoval(key string, removed bool) {
	s.retentionMu.Lock()
	if removed {
		s.retainVideoLocked(key, nil)
	}
	close(s.deletingVideos[key])
	delete(s.deletingVideos, key)
	s.retentionMu.Unlock()
	if removed {
		s.notifyResourceAvailability(key)
	}
}

// Explicit reconciliation is used at startup and by the periodic worker.
// Directory I/O and ranking never hold the lock needed to pin a video.
func (s *Server) trimCache() {
	s.runRetention(true)
}

func (s *Server) runRetention(reconcile bool) {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	if err := s.trimCachePass(reconcile); err != nil {
		s.cfg.Logger.Warn("cache_eviction_failed", "error", err)
	}
}

func (s *Server) requestRetentionLocked() {
	select {
	case s.retentionWake <- struct{}{}:
	default:
	}
}

func (s *Server) retentionLoop() {
	defer close(s.retentionDone)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	protectionTimer := time.NewTimer(time.Hour)
	defer protectionTimer.Stop()
	for {
		s.retentionMu.Lock()
		next := s.nextProtectionExpiryLocked(time.Now())
		s.retentionMu.Unlock()
		protectionTimer.Stop()
		var protectionExpired <-chan time.Time
		if !next.IsZero() {
			protectionTimer.Reset(time.Until(next))
			protectionExpired = protectionTimer.C
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.retentionWake:
			s.runRetention(false)
		case <-protectionExpired:
			s.runRetention(false)
		case <-ticker.C:
			s.runRetention(true)
		}
	}
}

// Called under retentionMu for every successful publication/removal, including
// superseded and invalid files. Changes made during a scan override its results.
func (s *Server) retainVideoLocked(key string, item *retainedVideo) {
	s.notifyBlockObservation()
	if s.retained == nil {
		s.retained = make(map[string]retainedVideo)
	}
	s.retainedBytes -= s.retained[key].size
	delete(s.retained, key)
	if item != nil {
		s.retained[key] = *item
		s.retainedBytes += item.size
	}
	if s.retentionChanges != nil {
		s.retentionChanges[key] = true
	}
}

func (s *Server) reconcileRetention() error {
	// Directory reconciliation can also discover externally removed files.
	var changed []string
	defer func() {
		if len(changed) > 0 {
			s.notifyResourceAvailability(changed...)
		}
	}()
	s.retentionMu.Lock()
	s.retentionChanges = make(map[string]bool)
	s.retentionMu.Unlock()
	defer func() {
		s.retentionMu.Lock()
		s.retentionChanges = nil
		s.retentionMu.Unlock()
	}()
	found := make(map[string]retainedVideo)
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
		item := retainedVideo{path: path, key: key, size: info.Size(), recent: info.ModTime().UnixMilli(), modified: info.ModTime().UnixNano()}
		found[key] = item
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
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	for key := range s.retentionChanges {
		delete(found, key)
		if item, ok := s.retained[key]; ok {
			found[key] = item
		}
	}
	for key := range s.retained {
		if _, exists := found[key]; !exists {
			changed = append(changed, key)
		}
	}
	s.retained = found
	s.retainedBytes = 0
	for _, item := range found {
		s.retainedBytes += item.size
	}
	return nil
}

func (s *Server) trimCachePass(reconcile bool) error {
	policies, err := s.resourcePolicies(context.Background())
	if err != nil {
		return err
	}
	s.reclaimBlocksWithPolicies(policies)
	s.retentionMu.Lock()
	limit := s.cfg.MaxCacheBytes
	unlimited := s.cfg.CacheUnlimited
	s.retentionMu.Unlock()
	// Resource-use coordination needs complete-file availability in every
	// budget mode. Unlimited skips eviction, not inventory reconciliation.
	if reconcile {
		if err := s.reconcileRetention(); err != nil {
			return err
		}
	}
	// Blocking is a resource removal policy, independent of capacity. Reuse
	// existing protection and deletion reservations, including active readers.
	s.retentionMu.Lock()
	var blocked []retainedVideo
	for key, item := range s.retained {
		if policies[key].blocked && !s.videoProtectedLocked(key) && s.beginVideoRemovalLocked(key) {
			blocked = append(blocked, item)
		}
	}
	s.retentionMu.Unlock()
	for _, item := range blocked {
		s.evictRetainedVideo(item)
	}
	if unlimited {
		return nil
	}
	s.retentionMu.Lock()
	total := s.retainedBytes
	var videos []retainedVideo
	if total > limit {
		for _, item := range s.retained {
			videos = append(videos, item)
		}
	}
	s.retentionMu.Unlock()
	if total <= limit {
		return nil
	}
	// Ranking is shared with admission before merge/publication.
	priorities, err := s.retentionPriorities(context.Background())
	if err != nil {
		return err
	}
	applyRetentionPriorities(videos, priorities)
	sort.Slice(videos, func(i, j int) bool { return lowerRetentionPriority(videos[i], videos[j]) })
	planned := total
	for _, item := range videos {
		if planned <= limit {
			break
		}
		s.retentionMu.Lock()
		// Active block protection outranks historical scores and does not count as
		// planned reclamation. Ordinary in-flight pins still defer eviction.
		if policies[item.key].favorite || s.videoProtectedLocked(item.key) {
			s.retentionMu.Unlock()
			continue
		}
		// A protected low-priority video is a deferred victim, not a reason
		// to evict a more valuable video while a prefetch is still finishing.
		planned -= item.size
		// Superseded-version cleanup may have already freed enough space
		// while this pass was reading statistics or ranking its snapshot.
		if s.retainedBytes <= limit {
			s.retentionMu.Unlock()
			break
		}
		current, exists := s.retained[item.key]
		if !exists || current.size != item.size || current.modified != item.modified || s.versionPins[item.key] > 0 {
			s.retentionMu.Unlock()
			continue
		}
		reserved := s.beginVideoRemovalLocked(item.key)
		s.retentionMu.Unlock()
		if !reserved {
			continue
		}
		s.evictRetainedVideo(item)
	}
	s.retentionMu.Lock()
	total = s.retainedBytes
	s.retentionMu.Unlock()
	if total > limit {
		s.cfg.Logger.Info("cache_limit_deferred", "retained_bytes", total, "limit_bytes", limit)
	}
	return nil
}

// The deletion reservation excludes new pins/publications for this key while
// filesystem calls run without the global retention lock.
func (s *Server) evictRetainedVideo(item retainedVideo) {
	removed := false
	defer func() { s.finishVideoRemoval(item.key, removed) }()
	info, err := os.Lstat(item.path)
	if os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		removed = true
		return
	}
	if err != nil {
		s.cfg.Logger.Warn("cache_eviction_failed", "path", item.path, "error", err)
		return
	}
	if info.Size() != item.size || info.ModTime().UnixNano() != item.modified {
		return
	}
	if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
		s.cfg.Logger.Warn("cache_eviction_failed", "path", item.path, "error", err)
		return
	}
	removed = true
	s.cfg.Logger.Info("cache_evicted", "key", item.key, "path", item.path, "bytes", item.size, "priority", item.score)
}
