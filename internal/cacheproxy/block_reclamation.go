package cacheproxy

import "time"

func (s *Server) zeroVideoBudget() bool {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	return !s.cfg.CacheUnlimited && s.cfg.MaxCacheBytes == 0
}

// A completed write can create an unprotected block (for example, when the
// download window starts before the independent local protection window).
func (s *Server) requestBlockReclamation() {
	s.retentionMu.Lock()
	if !s.cfg.CacheUnlimited && s.cfg.MaxCacheBytes == 0 {
		s.requestRetentionLocked()
	}
	s.retentionMu.Unlock()
}

// Protection decisions and removal from availability are one atomic operation.
// I/O references keep the exact detached files alive; a later store uses a new
// path and cannot be affected by this pass's unlocked filesystem cleanup.
func (s *Server) reclaimLocalBlocks() {
	type cleanup struct {
		blocks  *blockFiles
		paths   []string
		release bool
	}
	var pending []cleanup
	changed := false
	s.retentionMu.Lock()
	if s.cfg.CacheUnlimited || s.cfg.MaxCacheBytes != 0 {
		s.retentionMu.Unlock()
		return
	}
	now := time.Now()
	for key, local := range s.localResources {
		blocks := local.storage
		paths := blocks.retire(func(block int64) bool {
			remove := !s.blockProtectedLocked(key, block, now)
			changed = changed || remove
			return remove
		})
		blocks.mu.Lock()
		empty := len(blocks.files) == 0 && blocks.refs == 1
		blocks.mu.Unlock()
		if empty {
			delete(s.localResources, key)
		}
		pending = append(pending, cleanup{blocks, paths, empty})
	}
	s.retentionMu.Unlock()
	if changed {
		// Availability changed even when an I/O pin delays physical removal.
		// Wake both live sessions and continuing usage after session completion.
		s.notifyResourceAvailability()
	}
	for _, c := range pending {
		c.blocks.removeFiles(c.paths)
		if c.release {
			c.blocks.release()
		}
	}
}
