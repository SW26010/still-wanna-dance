package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

type preparationAdmissionKey struct{}
type preparationScopeKey struct{}

// One logical preparation, including address retries. The scope outlives its
// individual source registrations; only the engine allocates scheduling order.
type preparationScope struct {
	mu     sync.Mutex
	orders map[*Server]uint64
}

func withPreparationScope(ctx context.Context) context.Context {
	if _, ok := ctx.Value(preparationScopeKey{}).(*preparationScope); ok {
		return ctx
	}
	return context.WithValue(ctx, preparationScopeKey{}, &preparationScope{})
}

// Called only after successful usage reservation, with Server.mu held. Each
// engine owns its counter; sharing a caller context cannot mix their orders.
func (s *Server) preparationOrderLocked(ctx context.Context) (uint64, error) {
	p, _ := ctx.Value(preparationScopeKey{}).(*preparationScope)
	if p == nil {
		return 0, nil // Direct demand registration is not a manual preparation.
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if order := p.orders[s]; order != 0 {
		return order, nil
	}
	if s.preparationSeq == ^uint64(0) {
		return 0, errors.New("preparation order exhausted")
	}
	s.preparationSeq++
	if p.orders == nil {
		p.orders = make(map[*Server]uint64)
	}
	p.orders[s] = s.preparationSeq
	return s.preparationSeq, nil
}

type preparationAdmission struct {
	ready chan struct{}
	once  sync.Once
}

// WithPreparationAdmission separates source admission from the result returned
// by Prefetch. An ordered producer may resolve its next item once ready closes,
// while independently collecting this item's validation/publication result.
// On failure before admission ready stays open; the producer also waits for the
// operation's completion or cancellation. Retries share this notification and
// preparation scope. Calls without a notifier establish their own scope.
func WithPreparationAdmission(ctx context.Context) (context.Context, <-chan struct{}) {
	a := &preparationAdmission{ready: make(chan struct{})}
	// A producer creates a new logical operation, even under another scope.
	ctx = context.WithValue(ctx, preparationScopeKey{}, &preparationScope{})
	return context.WithValue(ctx, preparationAdmissionKey{}, a), a.ready
}

func preparationAdmitted(ctx context.Context) {
	if a, _ := ctx.Value(preparationAdmissionKey{}).(*preparationAdmission); a != nil {
		a.once.Do(func() { close(a.ready) })
	}
}

// Keep just the resolved next item pending at the producer. Source release and
// handoff expiry wake admission without re-querying metadata or polling.
func (s *Server) addPreparation(ctx context.Context, v video) (*downloadDemand, func(), error) {
	ctx = withPreparationScope(ctx)
	s.mu.Lock()
	resource := s.watchResourceLocked(v.key)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.unwatchResourceLocked(v.key)
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		capacity, changed := s.demandCapacityChangedLocked(), resource.changed
		s.mu.Unlock()
		// A capacity waiter has no registered source yet. Recheck admission
		// on every attempt; policy changes never revoke an admitted source.
		if v.songID != "" {
			id, _ := strconv.ParseInt(v.songID, 10, 64)
			if err := s.CheckSongAllowed(ctx, id); err != nil {
				return nil, nil, err
			}
		}
		d, release, err := s.addDemand(ctx, v, false)
		if !errors.Is(err, ErrDemandCapacity) {
			return d, release, err
		}
		if err := s.StorageError(); err != nil {
			return nil, nil, err
		}
		if blockCount(v.size, s.cfg.BlockSize) > backgroundUseBlocks {
			return nil, nil, errors.New("resource exceeds background block usage capacity")
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, nil, context.Canceled
		case <-changed:
		case <-capacity:
		}
	}
}
