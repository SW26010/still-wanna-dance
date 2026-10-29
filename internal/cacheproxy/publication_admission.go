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
// serialize with retention, while merge/hash I/O runs independently.
func (s *Server) reservePublication(ctx context.Context, v video) (*publicationReservation, error) {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.retentionMu.Lock()
	unlimited := s.cfg.CacheUnlimited
	limit := s.cfg.VideoBudgetBytes
	s.retentionMu.Unlock()
	policies, err := s.resourcePolicies(ctx)
	if err != nil {
		return nil, err
	}
	if policies[v.key].blocked || !policies[v.key].favorite && !unlimited && (limit == 0 || v.size > limit) {
		return nil, ErrNotRetained
	}
	var priorities map[string]ResourcePriority
	if !unlimited {
		var err error
		priorities, err = s.retentionPriorities(ctx)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now()
	child, cancel := context.WithCancelCause(ctx)
	r := &publicationReservation{item: retainedVideo{key: v.key, path: s.cfg.videoFile(v.key), size: v.size, recent: now.UnixMilli(), modified: now.UnixNano()}, ctx: child, cancel: cancel}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if s.publications == nil {
		s.publications = make(map[string]*publicationReservation)
	}
	// Concurrent callers are coalesced by obtain, before reaching publication.
	if s.publications[v.key] != nil {
		cancel(nil)
		return nil, errors.New("duplicate resource publication")
	}
	s.publications[v.key] = r
	s.selectPublicationsLocked(priorities, policies)
	if s.publications[v.key] != r {
		return nil, ErrNotRetained
	}
	return r, nil
}

// Retained protected videos are fixed occupants. Other videos and pending
// publications use the existing eviction order; a better candidate can displace
// a lower reservation. Ordinary file pins defer physical removal, not ranking.
func (s *Server) selectPublicationsLocked(priorities map[string]ResourcePriority, policies map[string]resourcePolicy) {
	// Even unlimited storage cannot publish an entirely blocked resource.
	for key, r := range s.publications {
		if policies[key].blocked {
			delete(s.publications, key)
			r.cancel(ErrNotRetained)
		}
	}
	if s.cfg.CacheUnlimited {
		return
	}
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
	sort.Slice(videos, func(i, j int) bool { return lowerRetentionPriority(videos[i], videos[j]) })
	for _, item := range videos {
		if total <= s.cfg.VideoBudgetBytes {
			break
		}
		if policies[item.key].favorite {
			continue
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
}

func (s *Server) releasePublication(r *publicationReservation) {
	s.retentionMu.Lock()
	if s.publications[r.item.key] == r {
		delete(s.publications, r.item.key)
	}
	s.retentionMu.Unlock()
	r.cancel(nil)
}

func (s *Server) commitPublication(r *publicationReservation, file *os.File, path string, v video, f *flight) error {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return context.Cause(r.ctx)
	}
	policies, err := s.resourcePolicies(r.ctx)
	if err != nil {
		return err
	}
	s.retentionMu.Lock()
	unlimited := s.cfg.CacheUnlimited
	s.retentionMu.Unlock()
	var priorities map[string]ResourcePriority
	if !unlimited {
		var err error
		priorities, err = s.retentionPriorities(r.ctx)
		if err != nil {
			return err
		}
	}
	s.retentionMu.Lock()
	s.selectPublicationsLocked(priorities, policies)
	accepted := s.publications[v.key] == r
	s.retentionMu.Unlock()
	if !accepted {
		return ErrNotRetained
	}
	// No other admission/retention pass can change this decision until inventory
	// settlement. The worker's video pin also excludes explicit file deletion.
	return s.commitPublishedFile(r.ctx, file, path, v, f)
}
