package cacheproxy

import "time"

func (s *Server) requestBlockReclamation(key string) {
	s.retentionMu.Lock()
	s.requestResourceRetentionLocked(key)
	s.retentionMu.Unlock()
}

// Protection decisions and removal from availability are one atomic operation.
// I/O references keep the exact detached files alive; a later store uses a new
// path and cannot be affected by this pass's unlocked filesystem cleanup.
func (s *Server) reclaimLocalBlocks() {
	s.reclaimSelectedBlocks(nil)
}

func (s *Server) reclaimSelectedBlocks(keys map[string]bool) {
	type cleanup struct {
		blocks  *blockFiles
		files   []*localBlockFile
		release bool
	}
	var pending []cleanup
	var changed []string
	// Keep the decision atomic with both source changes and grace updates.
	s.mu.Lock()
	s.retentionMu.Lock()
	now := time.Now()
	reclaim := func(key string, local *localResource) {
		blocks := local.storage
		removed := false
		files := blocks.retire(func(block int64) bool {
			remove := s.useBlocks[useBlockID{key, block}] == 0 && !s.blockProtectedLocked(key, block, now)
			removed = removed || remove
			return remove
		})
		if removed {
			changed = append(changed, key)
		}
		empty := blocks.emptyAndUnshared()
		if empty {
			delete(s.localResources, key)
		}
		pending = append(pending, cleanup{blocks, files, empty})
	}
	if keys == nil {
		for key, local := range s.localResources {
			reclaim(key, local)
		}
	} else {
		for key := range keys {
			if local := s.localResources[key]; local != nil {
				reclaim(key, local)
			}
		}
	}
	s.retentionMu.Unlock()
	s.mu.Unlock()
	if len(changed) > 0 {
		// Availability changed even when an I/O pin delays physical removal.
		// Wake both live sessions and continuing usage after session completion.
		s.notifyResourceAvailability(changed...)
	}
	for _, c := range pending {
		c.blocks.removeFiles(c.files)
		if c.release {
			c.blocks.release()
		}
	}
}
