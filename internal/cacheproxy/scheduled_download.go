package cacheproxy

import (
	"context"
	"errors"
	"sort"
	"time"

	"still-wanna-dance/internal/blocksched"
)

const (
	sourceLease         = 30 * time.Second
	sourceRenewInterval = sourceLease / 3
)

type scheduledBlock struct {
	task       *blocksched.Task
	merged     *blocksched.Demand
	published  blocksched.Need
	nextExpiry time.Time
	retiring   bool
}

// A plan keeps the resource change notification captured with its blocks.
type sessionPlan struct {
	blocks  map[int64]blockUse
	changed <-chan struct{}
	at      time.Time
	active  bool
}

type downloadSession struct {
	s          *Server
	ctx        context.Context
	v          video
	f          *flight
	wake       chan struct{}
	reads      *blocksched.ReadRequests
	jobs       map[int64]*scheduledBlock // resource coordinator only
	finished   map[int64]bool            // Completed work in this session; independent of later aggressive reclamation.
	capacity   <-chan struct{}
	refetchAt  time.Time // Earliest protection entry of missing completed work; recomputed by plan.
	changes    *resourceChange
	leaseUntil time.Time // Stable between confirmations; unrelated events cannot renew it.
}

func newDownloadSession(s *Server, ctx context.Context, v video, f *flight) *downloadSession {
	w := &downloadSession{s: s, ctx: ctx, v: v, f: f, wake: make(chan struct{}, 1), jobs: make(map[int64]*scheduledBlock)}
	w.reads = s.scheduler.NewReadRequests(v.key, func(block int64) string { return downloadTaskKey(w.v.key, w.v.size, block) }, w.wake)
	s.mu.Lock()
	w.changes = s.watchResourceLocked(v.key)
	s.mu.Unlock()
	return w
}
func (w *downloadSession) exposeLocalReads(d *localResource) *spool {
	if w.f.spool != nil {
		return w.f.spool
	}
	if !w.f.discard {
		w.f.spool = d.makeSpool(func(ctx context.Context, offset int64) func() { return w.reads.Wait(ctx, offset/w.s.cfg.BlockSize) })
	}
	close(w.f.streaming)
	return w.f.spool
}

// run joins the task and session lifetimes before handing admitted work to the
// executor. Demand planning and task registration stay in the coordinator.
func (w *downloadSession) run(ctx context.Context, p *blocksched.Progress, block int64) blocksched.Result {
	if w.ctx.Err() != nil {
		return blocksched.Result{Err: context.Cause(w.ctx)}
	}
	if deadline, ok := w.ctx.Deadline(); ok {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, deadline)
		defer stop()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(w.ctx, func() { cancel(context.Cause(w.ctx)) })
	defer stop()
	defer cancel(nil)
	return blocksched.Result{Err: w.s.readRange(ctx, w.f, block, p)}
}

func (w *downloadSession) submit(block int64, j *scheduledBlock, n blocksched.Need) error {
	key := w.reads.Key(block)
	remaining := blockBytes(w.v.size, block, w.s.cfg.BlockSize)
	t, d, err := w.s.scheduler.Schedule(key, remaining, func(ctx context.Context, p *blocksched.Progress) blocksched.Result { return w.run(ctx, p, block) }, n)
	if err != nil {
		return err
	}
	t.Watch(w.wake)
	j.task, j.merged, j.published = t, d, n
	w.jobs[block] = j
	w.logDemand(block, j)
	return nil
}

func (w *downloadSession) plan(now time.Time) (map[int64]blockUse, <-chan struct{}, bool) {
	w.refetchAt = time.Time{}
	if !w.nextRenewal().After(now) {
		w.leaseUntil = now.Add(sourceLease)
	}
	blocks := blockCount(w.v.size, w.s.cfg.BlockSize)
	w.s.mu.Lock()
	plan, active := w.s.usePlanLocked(w.v.key, now)
	// Projection still checks actual source exits/expiry at now. Cap live
	// leases to this confirmation's fixed horizon; shorter handoffs retain
	// their original deadlines and continue to contribute nextExpiry.
	for block, use := range plan {
		if use.need.LeaseUntil.After(w.leaseUntil) {
			use.need.LeaseUntil = w.leaseUntil
		}
		if use.nextExpiry.After(w.leaseUntil) {
			use.nextExpiry = w.leaseUntil
		}
		plan[block] = use
	}
	changed := w.changes.changed
	w.s.mu.Unlock()
	requested := w.reads.Requested()
	for block := range requested {
		if _, ok := plan[block]; !ok {
			plan[block] = blockUse{} // The scheduler owns the independent read request.
		}
	}
	local := w.f.ranges
	aggressiveReclamation := w.s.aggressiveBlockReclamation()
	for b, use := range plan {
		// A completed write can precede Done. Keep an existing execution's
		// demand through that final cleanup; availability prevents new work,
		// rather than canceling a successful task before its result is observed.
		if b < 0 || b >= blocks {
			delete(plan, b)
			continue
		}
		if local != nil && local.has(b) {
			if w.jobs[b] == nil {
				delete(plan, b)
			}
			continue
		}
		if aggressiveReclamation && w.finished[b] && !requested[b] {
			// Completion prevents an unprotected refill/evict loop, not a new
			// finite use of missing data. Protection comes from the same usage
			// summary; its tail alone cannot create download qualification.
			if at := use.refetchAt(w.s.protectionAhead()); !at.IsZero() {
				if !at.After(now) {
					continue // Normal scheduler window, deadline and quota still apply.
				}
				w.refetchAt = earlier(w.refetchAt, at)
			}
			delete(plan, b)
		}
	}
	return plan, changed, active
}

// All source transitions for one block are merged before a single scheduler
// update. The scheduler never observes the intermediate GET/handoff ordering.
func (w *downloadSession) reconcile(plan map[int64]blockUse, now time.Time, changed <-chan struct{}, active bool) (time.Time, bool, error) {
	var until time.Time
	capacity := w.s.scheduler.CapacityChanged()
	w.capacity = nil
	order := make([]int64, 0, len(plan))
	for block := range plan {
		order = append(order, block)
	}
	w.reads.OrderRegistration(order)
	for _, block := range order {
		j := w.jobs[block]
		if j == nil {
			j = &scheduledBlock{}
		}
		if j.task == nil {
			j.nextExpiry = plan[block].nextExpiry
			if err := w.submit(block, j, plan[block].need); err != nil {
				if errors.Is(err, blocksched.ErrCapacity) {
					w.capacity = capacity
					continue
				}
				if errors.Is(err, blocksched.ErrExpired) {
					w.s.expireDownload(w.f, w.v.key, block)
					return until, true, nil // Replan after source exits; keep unrelated executions.
				}
				return until, false, err
			}
		}
	}
	order = order[:0]
	for block := range w.jobs {
		order = append(order, block)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	updates := make(map[int64]blocksched.Need, len(order))
	for _, block := range order {
		j := w.jobs[block]
		if j.retiring {
			continue
		}
		j.nextExpiry = plan[block].nextExpiry
		n := plan[block].need
		until = maxTime(until, n.LeaseUntil)
		updates[block] = n
	}

	// Stop accepting readers before canceling the last tasks or waiting for
	// their bodies and writes to close. A concurrent GET invalidates an idle
	// snapshot and must be planned before any irreversible task retirement.
	if !active && len(plan) == 0 && !until.After(now) && !w.s.preemptFlight(w.f, changed) {
		return until, true, nil
	}
	for _, block := range order {
		n, ok := updates[block]
		if !ok {
			continue
		}
		j := w.jobs[block]
		if n.Owner == "" || !n.LeaseUntil.After(now) {
			if j.merged != nil {
				j.merged.Close()
				j.merged = nil
			}
			j.published = blocksched.Need{}
			// The scheduler decides whether independent local reads still need
			// this execution. Withdrawing ordinary usage cannot cancel them.
			if j.task == nil {
				delete(w.jobs, block)
			}
			continue
		}
		if j.task == nil {
			continue // Registration above is waiting for scheduler capacity.
		}
		if j.merged != nil && j.published == n {
			continue
		}
		if j.merged == nil || !j.merged.Update(n) {
			j.merged = j.task.Add(n)
			if !j.merged.Accepted() {
				// A retired task will report Done after cleanup. A task kept
				// alive by local reads can instead be waiting for ordinary
				// demand capacity; failed registration must not cancel it.
				j.merged = nil
				w.capacity = capacity
				continue
			}
		}
		j.published = n
		w.logDemand(block, j)
	}
	return until, false, nil
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// reapCompleted drains task results after response and write cleanup.
func (w *downloadSession) reapCompleted() error {
	for block, j := range w.jobs {
		if j.task == nil {
			continue
		}
		select {
		case <-j.task.Done():
			if err := j.task.Err(); errors.Is(err, blocksched.ErrExpired) && !errors.Is(err, errUpstreamDownload) {
				w.s.expireDownload(w.f, w.v.key, block)
				delete(w.jobs, block)
				continue
			}
			if errors.Is(j.task.Err(), blocksched.ErrDeferred) {
				j.task, j.merged = nil, nil
				continue
			}
			// Lease expiry can finish before the coordinator marks retirement.
			// The scheduler's plain cancellation is still a stopped old task;
			// transport failures carry their own upstream error marker.
			j.retiring = j.retiring || j.task.Err() == context.Canceled
			if err := j.task.Err(); err != nil && !(j.retiring && errors.Is(err, context.Canceled)) {
				return err
			}
			delete(w.jobs, block)
			if j.task.Err() == nil && !w.f.discard && w.s.aggressiveBlockReclamation() {
				if w.finished == nil {
					w.finished = make(map[int64]bool)
				}
				w.finished[block] = true
			}
		default:
		}
	}
	return nil
}

func (w *downloadSession) close(result error) error {
	defer w.releaseResourceWatch()
	w.reads.Close()
	if errors.Is(result, errPreempted) {
		w.s.preemptFlight(w.f, nil)
	}
	for _, j := range w.jobs {
		if j.task != nil {
			j.task.Close()
		}
	}
	for _, j := range w.jobs {
		if j.task != nil && !errors.Is(j.task.Err(), blocksched.ErrDeferred) {
			result = preferDownloadError(result, j.task.Err())
		}
	}
	return result
}

func (w *downloadSession) releaseResourceWatch() {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.changes != nil {
		w.s.unwatchResourceLocked(w.v.key)
		w.changes = nil
	}
}

func (w *downloadSession) nextPlan() (sessionPlan, error) {
	if w.ctx.Err() != nil {
		return sessionPlan{}, context.Cause(w.ctx)
	}
	if err := w.reapCompleted(); err != nil {
		return sessionPlan{}, err
	}
	now := time.Now()
	blocks, changed, active := w.plan(now)
	return sessionPlan{blocks: blocks, changed: changed, at: now, active: active}, nil
}

func (w *downloadSession) waitForChanges(timer *time.Timer, plan sessionPlan, until time.Time) {
	wait := w.nextRenewal().Sub(plan.at)
	if w.refetchAt.After(plan.at) {
		wait = min(wait, w.refetchAt.Sub(plan.at))
	}
	if !plan.active && len(plan.blocks) == 0 {
		wait = min(wait, until.Sub(plan.at))
	}
	for _, j := range w.jobs {
		if j.nextExpiry.After(plan.at) {
			wait = min(wait, j.nextExpiry.Sub(plan.at))
		}
	}
	timer.Reset(max(time.Millisecond, wait))
	select {
	case <-w.ctx.Done():
	case <-w.wake:
	case <-plan.changed:
	case <-w.capacity:
	case <-timer.C:
	}
}

func (s *Server) publishScheduled(ctx context.Context, path string, v video, f *flight) (result error) {
	if f.ranges == nil {
		if f.discard {
			f.ranges = &localResource{blockSize: s.cfg.BlockSize, video: resourceVideo(v), done: make(map[int64]bool)}
		} else {
			var err error
			f.ranges, err = s.newLocalResource(v, f.log)
			if err != nil {
				return err
			}
		}
	}
	w := newDownloadSession(s, ctx, v, f)
	defer func() { result = w.close(result) }()
	w.exposeLocalReads(f.ranges)
	f.progress.setStage("range_download")
	timer := time.NewTimer(sourceRenewInterval)
	defer timer.Stop()
	for {
		plan, err := w.nextPlan()
		if err != nil {
			return err
		}
		// Planning filters completed blocks, so an empty plan can mean the
		// last range just finished. Check completion after that filtering and
		// before withdrawing demands; drain task results before hashing.
		complete := f.ranges.complete()
		if !complete && !f.discard && s.aggressiveBlockReclamation() {
			complete = len(plan.blocks) == 0 && w.refetchAt.IsZero() && w.allFetched()
			if complete && len(w.jobs) == 0 && !s.preemptFlight(f, plan.changed) {
				continue // A new source invalidated completion based on old work.
			}
		}
		if complete && len(w.jobs) == 0 {
			break
		}
		// A reader can accept the last bytes before the executor reports Done.
		// Drain that execution and its publication instead of treating delivery
		// as abandonment.
		select {
		case <-f.streaming:
			complete = complete || f.spool != nil && f.spool.hasAllBytes(v.size)
		default:
		}
		if complete {
			select {
			case <-ctx.Done():
			case <-w.wake:
			}
			continue
		}
		until, retry, err := w.reconcile(plan.blocks, plan.at, plan.changed, plan.active)
		if err != nil {
			return err
		}
		if retry {
			continue
		}
		if !plan.active && len(plan.blocks) == 0 && !until.After(plan.at) {
			return errPreempted
		}
		w.waitForChanges(timer, plan, until)
	}
	// Hashing and publication stay bounded without charging the preceding
	// intentional waits between playback-driven block executions.
	if f.discard {
		return nil
	}
	ctx, stopPublication := context.WithTimeout(ctx, s.cfg.DownloadTimeout)
	defer stopPublication()
	return s.publishBlocks(ctx, path, v, f)
}

func (w *downloadSession) nextRenewal() time.Time {
	return w.leaseUntil.Add(sourceRenewInterval - sourceLease)
}

// Historical completion only permits exit when plan has no remaining downloads
// or future refetch window. It never establishes current local availability.
func (w *downloadSession) allFetched() bool {
	for block := int64(0); block < blockCount(w.v.size, w.s.cfg.BlockSize); block++ {
		if !w.finished[block] && !w.f.ranges.has(block) {
			return false
		}
	}
	return true
}
