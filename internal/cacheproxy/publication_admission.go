package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// Temporary readability is not success for manual cache completion.
// A batch stops normally instead of trying another route for a retention decision.
var ErrNotRetained = fmt.Errorf("%w：资源未取得持久化保留资格", ErrBatchBudget)

type publicationReservation struct {
	item   retainedVideo
	ctx    context.Context
	cancel context.CancelCauseFunc
}

// One shared resource flight owns one reservation. Admission and final rename
// serialize with retention, while merge/hash and disk preparation run independently.
func (s *Server) reservePublication(ctx context.Context, v video) (result *publicationReservation, resultErr error) {
	resultErr = s.Store.retentionDecision(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.retentionMu.Lock()
		unlimited := s.cfg.CacheUnlimited
		limit := s.cfg.VideoBudgetBytes
		s.retentionMu.Unlock()
		policies, err := s.resourcePolicies(ctx)
		if err != nil {
			return err
		}
		if policies[v.key].blocked || !unlimited && (limit == 0 || v.size > limit) {
			return ErrNotRetained
		}
		now := time.Now()
		child, cancel := context.WithCancelCause(ctx)
		r := &publicationReservation{item: retainedVideo{key: v.key, path: s.cfg.videoFile(v.key), size: v.size, recent: now.UnixMilli(), modified: now.UnixNano()}, ctx: child, cancel: cancel}
		s.retentionMu.Lock()
		if s.publications == nil {
			s.publications = make(map[string]*publicationReservation)
		}
		// Concurrent callers are coalesced by obtain, before reaching publication.
		if s.publications[v.key] != nil {
			s.retentionMu.Unlock()
			cancel(nil)
			return errors.New("duplicate resource publication")
		}
		s.publications[v.key] = r
		s.retentionMu.Unlock()
		if err := s.selectPublications(ctx, policies); err != nil {
			s.releasePublication(r)
			return err
		}
		s.retentionMu.Lock()
		defer s.retentionMu.Unlock()
		if s.publications[v.key] != r {
			return ErrNotRetained
		}
		result = r
		return nil
	})
	return result, resultErr
}

// Retained protected videos are fixed occupants. Other videos and pending
// publications use the existing eviction order; a better candidate can displace
// a lower reservation. Ordinary file pins defer physical removal, not ranking.
// The caller holds Store's retention decision lock through admission or commit.
// Inventory can only shrink while priorities are loaded without retentionMu.
func (s *Server) selectPublications(ctx context.Context, policies map[string]resourcePolicy) error {
	s.retentionMu.Lock()
	// Even unlimited storage cannot publish an entirely blocked resource.
	for key, r := range s.publications {
		if policies[key].blocked {
			delete(s.publications, key)
			r.cancel(ErrNotRetained)
		}
	}
	if s.cfg.CacheUnlimited || !s.publicationBudgetExceededLocked() {
		s.retentionMu.Unlock()
		return nil
	}
	s.retentionMu.Unlock()
	priorities, err := s.retentionPriorities(ctx)
	if err != nil {
		return err
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	videos := make([]retainedVideo, 0, len(s.retained)+len(s.publications))
	var total int64
	for _, item := range s.retained {
		videos = append(videos, item)
		total += item.size
	}
	selected := make(map[string]bool, len(s.publications))
	for key, r := range s.publications {
		selected[key] = true
		if _, exists := s.retained[key]; !exists {
			videos = append(videos, r.item)
			total += r.item.size
		}
	}
	applyRetentionPriorities(videos, priorities)
	sort.Slice(videos, func(i, j int) bool {
		return lowerRetentionPriority(videos[i], videos[j], policies, s.cfg.VideoBudgetBytes)
	})
	for _, item := range videos {
		if total <= s.cfg.VideoBudgetBytes {
			break
		}
		if _, exists := s.retained[item.key]; exists && s.videoProtectedLocked(item.key) {
			continue
		}
		total -= item.size
		delete(selected, item.key)
	}
	for key, r := range s.publications {
		if !selected[key] {
			delete(s.publications, key)
			r.cancel(ErrNotRetained)
		}
	}
	return nil
}

// Formal inventory already accounts for reservations that have been published.
// Subtract from the remaining budget to avoid overflowing a sum of reservations.
func (s *Server) publicationBudgetExceededLocked() bool {
	remaining := s.cfg.VideoBudgetBytes - s.retainedBytes
	if remaining < 0 {
		return true
	}
	for key, r := range s.publications {
		if _, exists := s.retained[key]; exists {
			continue
		}
		if r.item.size > remaining {
			return true
		}
		remaining -= r.item.size
	}
	return false
}

func (s *Server) releasePublication(r *publicationReservation) {
	s.retentionMu.Lock()
	if s.publications[r.item.key] == r {
		delete(s.publications, r.item.key)
	}
	s.retentionMu.Unlock()
	r.cancel(nil)
}

func (s *Server) commitPreparedPublication(r *publicationReservation, source string, item retainedVideo) error {
	return s.Store.retentionDecision(func() error {
		if err := r.ctx.Err(); err != nil {
			return context.Cause(r.ctx)
		}
		policies, err := s.resourcePolicies(r.ctx)
		if err != nil {
			return err
		}
		if err := s.selectPublications(r.ctx, policies); err != nil {
			return err
		}
		s.retentionMu.Lock()
		accepted := s.publications[item.key] == r
		s.retentionMu.Unlock()
		if !accepted {
			return ErrNotRetained
		}
		if err := r.ctx.Err(); err != nil {
			return context.Cause(r.ctx)
		}
		// No other admission/retention pass can change this decision until inventory
		// settlement. The worker's video pin also excludes explicit file deletion.
		if err := os.Rename(source, item.path); err != nil {
			return err
		}
		s.retentionMu.Lock()
		s.retainVideoLocked(item.key, &item)
		if !s.cfg.CacheUnlimited && s.retainedBytes > s.cfg.VideoBudgetBytes {
			s.requestRetentionLocked()
		}
		s.retentionMu.Unlock()
		return nil
	})
}
