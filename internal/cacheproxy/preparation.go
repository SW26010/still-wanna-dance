package cacheproxy

import (
	"context"
	"errors"
	"sync"
)

type preparationAdmissionKey struct{}

type preparationAdmission struct {
	ready chan struct{}
	once  sync.Once
}

// WithPreparationAdmission separates source admission from the result returned
// by Prefetch. An ordered producer may resolve its next item once ready closes,
// while independently collecting this item's validation/publication result.
// On failure before admission ready stays open; the producer also waits for the
// operation's completion or cancellation. Retries share this one notification.
func WithPreparationAdmission(ctx context.Context) (context.Context, <-chan struct{}) {
	a := &preparationAdmission{ready: make(chan struct{})}
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
	for {
		changed := s.DemandCapacityChanged()
		d, release, err := s.addDemand(ctx, v, false)
		if !errors.Is(err, ErrDemandCapacity) {
			return d, release, err
		}
		if err := s.StorageError(); err != nil {
			return nil, nil, err
		}
		if (v.size+rangeBlockSize-1)/rangeBlockSize > backgroundUseBlocks {
			return nil, nil, errors.New("resource exceeds background block usage capacity")
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, nil, context.Canceled
		case <-changed:
		}
	}
}
