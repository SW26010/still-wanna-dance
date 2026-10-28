package cacheproxy

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

type DownloadFillFailure struct {
	SongID string
	Err    error
}

// DownloadFillResult counts songs, including aliases of a shared resource.
// An admitted resource contributes no result until its preparation finishes.
// Failures is append-only across progress snapshots of the same Run.
type DownloadFillResult struct {
	Total, Checked, Hits, Downloaded, Failed, Skipped int
	Missing, Reused, CatalogHits                      int
	BudgetReached                                     bool
	CurrentSongID                                     string
	Failures                                          []DownloadFillFailure
}

// DownloadFill owns one fill's resource order, retry scopes and settlement.
// Console owns catalog refresh, task cancellation and presentation.
type DownloadFill struct {
	server          *Server
	ctx             context.Context
	scanOnly        bool
	retainFavorites bool
}

// NewDownloadFill captures the initial budget before catalog refresh or other
// work can change inventory. Run the operation once with a pinned catalog.
func (s *Server) NewDownloadFill(ctx context.Context, scanOnly, retainFavorites bool) *DownloadFill {
	if !scanOnly {
		ctx = s.withBatchBudget(ctx)
	}
	return &DownloadFill{server: s, ctx: ctx, scanOnly: scanOnly, retainFavorites: retainFavorites}
}

type fillResource struct {
	md5   string
	ids   []string
	score float64
}

func (s *Server) fillResources(ctx context.Context, references map[string]string) ([]*fillResource, error) {
	priorities, err := s.EffectiveResourcePriorities(ctx, references)
	if err != nil {
		return nil, err
	}
	grouped := map[string]*fillResource{}
	for id, key := range references {
		if grouped[key] == nil {
			// Retained mappings still contribute to shared-resource priority.
			grouped[key] = &fillResource{md5: key, score: priorities[key].Score}
		}
		grouped[key].ids = append(grouped[key].ids, id)
	}
	newer := func(a, b string) bool {
		x, _ := strconv.ParseInt(a, 10, 64)
		y, _ := strconv.ParseInt(b, 10, 64)
		return x > y
	}
	jobs := make([]*fillResource, 0, len(grouped))
	for _, job := range grouped {
		sort.Slice(job.ids, func(i, j int) bool { return newer(job.ids[i], job.ids[j]) })
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].score != jobs[j].score {
			return jobs[i].score > jobs[j].score
		}
		return newer(jobs[i].ids[0], jobs[j].ids[0])
	})
	return jobs, nil
}

// Run uses the existing song preparation adapter, forwarding an opaque context
// through all retries. The adapter returns the engine's PrefetchSong result.
// Progress snapshots are serialized, independent copies; the callback must
// return promptly. Run waits for every started preparation and retry budget
// scope to settle, including on cancellation. Shared engine work may outlive it.
func (f *DownloadFill) Run(local LocalCatalog, prefetch func(context.Context, int64) (string, error), progress func(DownloadFillResult)) (DownloadFillResult, error) {
	s, ctx := f.server, f.ctx
	var result DownloadFillResult
	jobs, err := s.fillResources(ctx, local.Files.References)
	if err != nil {
		return result, err
	}
	var mu sync.Mutex
	update := func(change func()) {
		mu.Lock()
		defer mu.Unlock()
		change()
		if progress != nil {
			snapshot := result
			snapshot.Failures = slices.Clone(result.Failures)
			progress(snapshot)
		}
	}
	update(func() { result.Total = len(local.Files.References) })
	var budgetStopped atomic.Bool
	process := func(ctx context.Context, job *fillResource) {
		if ctx.Err() != nil {
			return
		}
		var err error
		ids := slices.Clone(job.ids)
		skip := func(id string) {
			ids = slices.DeleteFunc(ids, func(candidate string) bool { return candidate == id })
			update(func() { result.Checked++; result.Skipped++ })
		}
		if !f.scanOnly {
			var policies map[int64]SongPolicy
			policies, err = s.SongPolicies(ctx)
			favorite := false
			if err == nil {
				for _, id := range job.ids {
					n, _ := strconv.ParseInt(id, 10, 64)
					if policies[n] == SongBlocked {
						skip(id)
					}
					favorite = favorite || f.retainFavorites && policies[n] == SongFavorite
				}
			}
			if len(ids) == 0 || budgetStopped.Load() && !favorite && err == nil {
				return
			}
		}
		hit := local.Files.Present[job.md5]
		source := "HIT"
		if err == nil && !hit && !f.scanOnly {
			// One reservation scope covers every candidate for this resource.
			// Admission can release dispatch before retries and settlement end.
			songCtx, finish := withBatchSongBudget(ctx)
			defer finish()
			songCtx = withExpectedMD5(songCtx, job.md5)
			candidates := slices.Clone(ids)
			sort.SliceStable(candidates, func(i, j int) bool {
				return local.Current[candidates[i]] && !local.Current[candidates[j]]
			})
			for _, id := range candidates {
				n, _ := strconv.ParseInt(id, 10, 64)
				update(func() { result.CurrentSongID = id })
				attemptSource, attemptErr := prefetch(songCtx, n)
				if errors.Is(attemptErr, ErrSongBlocked) {
					skip(id)
					continue
				}
				source, err = attemptSource, attemptErr
				if err == nil || songCtx.Err() != nil || errors.Is(err, ErrBatchBudget) || errors.Is(err, ErrLocalStorage) {
					break
				}
			}
			if len(ids) == 0 {
				return
			}
			if errors.Is(err, ErrBatchBudget) {
				budgetStopped.Store(true)
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		update(func() {
			result.Checked += len(ids)
			switch {
			case err != nil:
				result.Failed += len(ids)
				for _, id := range ids {
					result.Failures = append(result.Failures, DownloadFillFailure{SongID: id, Err: err})
				}
			case hit:
				result.Hits += len(ids)
				result.Reused += len(ids)
				result.CatalogHits += len(ids)
			case f.scanOnly:
				result.Missing += len(ids)
			case source == "HIT":
				result.Hits += len(ids)
			default:
				result.Downloaded += len(ids)
			}
		})
	}
	var wg sync.WaitGroup
dispatch:
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		jobCtx, admitted := WithPreparationAdmission(ctx)
		finished := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(finished)
			process(jobCtx, job)
		}()
		// Early failure and local hits can finish without admission. Otherwise
		// only usage admission allows the next ordered resource to resolve.
		select {
		case <-admitted:
		case <-finished:
		case <-ctx.Done():
			break dispatch
		}
	}
	wg.Wait()
	update(func() {
		result.BudgetReached = budgetStopped.Load()
		if result.BudgetReached || ctx.Err() == nil {
			result.CurrentSongID = ""
		}
	})
	return result, ctx.Err()
}
