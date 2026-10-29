package cacheproxy

import "time"

// Account at the file owner, not at a demand or registry entry: old readers can
// outlive both. Unsubscribed engines maintain only this byte total, with no I/O
// or observation worker. Failed deletion deliberately keeps its charge.
func (s *Server) observeBlockCacheBytes(delta int64) {
	if delta == 0 {
		return
	}
	s.blockCacheBytes.Add(delta)
	if o := s.blockObserver.Load(); o != nil {
		select {
		case o.cacheWake <- struct{}{}:
		default:
		}
	}
}

func (s *BlockSubscription) BlockCacheBytes() (int64, <-chan struct{}) {
	o := s.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.s.blockCacheBytes.Load(), o.cacheChanged
}

// Coalesce file changes without rebuilding demand snapshots. There is no idle
// sampling; every new connection reads the current total even with no demands.
func (o *blockObserver) runBlockCache() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-o.cacheWake:
		}
		timer.Reset(50 * time.Millisecond)
		select {
		case <-o.ctx.Done():
			return
		case <-timer.C:
		}
		select {
		case <-o.cacheWake:
		default:
		}
		o.mu.Lock()
		close(o.cacheChanged)
		o.cacheChanged = make(chan struct{})
		o.mu.Unlock()
	}
}
