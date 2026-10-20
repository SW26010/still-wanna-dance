package cacheproxy

import "time"

func (c Config) aggressiveBlockReclamation() bool {
	return c.AggressiveCacheReclamation == "on" ||
		c.AggressiveCacheReclamation == "auto" && !c.CacheUnlimited && c.MaxCacheBytes == 0
}

func (s *Server) aggressiveBlockReclamation() bool {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	return s.cfg.aggressiveBlockReclamation()
}

// A completed write can create an unprotected block (for example, when the
// download window starts before the independent local protection window).
func (s *Server) requestBlockReclamation() {
	s.retentionMu.Lock()
	if s.cfg.aggressiveBlockReclamation() {
		s.requestRetentionLocked()
	}
	s.retentionMu.Unlock()
}

// Protection decisions and removal from availability are one atomic operation.
// I/O references keep the exact detached files alive; a later store uses a new
// path and cannot be affected by this pass's unlocked filesystem cleanup.
func (s *Server) reclaimLocalBlocks() {
	s.reclaimBlocksWithPolicies(nil)
}

func (s *Server) reclaimBlocksWithPolicies(policies map[string]resourcePolicy) {
	type cleanup struct {
		blocks  *blockFiles
		paths   []string
		release bool
	}
	var pending []cleanup
	changed := false
	s.retentionMu.Lock()
	aggressive := s.cfg.aggressiveBlockReclamation()
	now := time.Now()
	for key, local := range s.localResources {
		// A policy change does not interrupt work already using this resource.
		if !aggressive && (!policies[key].blocked || s.versionPins[key] > 0) {
			continue
		}
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
