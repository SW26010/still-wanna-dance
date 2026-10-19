package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"still-wanna-dance/internal/blocksched"
)

const sourceLease = 30 * time.Second
const getHandoff = 2 * time.Second
const queueHandoff = 20 * time.Second

type scheduledBlock struct {
	task       *blocksched.Task
	merged     *blocksched.Demand
	published  blocksched.Need
	nextExpiry time.Time
	retiring   bool
}
type downloadSession struct {
	s           *Server
	ctx         context.Context
	v           video
	f           *flight
	wake        chan struct{}
	initialized chan struct{}
	initOnce    sync.Once
	reads       *blocksched.ReadRequests
	jobs        map[int64]*scheduledBlock // resource coordinator only
	finished    map[int64]bool            // Completed work in this session; independent of later aggressive reclamation.
	capacity    <-chan struct{}
	refetchAt   time.Time // Earliest protection entry of missing completed work; recomputed by plan.
}

func newDownloadSession(s *Server, ctx context.Context, v video, f *flight) *downloadSession {
	w := &downloadSession{s: s, ctx: ctx, v: v, f: f, wake: make(chan struct{}, 1), initialized: make(chan struct{}), jobs: make(map[int64]*scheduledBlock)}
	w.reads = s.scheduler.NewReadRequests(v.key, func(block int64) string { return fmt.Sprintf("%s/%d/%d", w.v.key, w.v.size, block) }, w.wake)
	return w
}
func (w *downloadSession) initialize(d *localResource) {
	w.f.ranges = d
	if !w.f.discard {
		w.f.spool = d.makeSpool(func(offset int64) func() { return w.reads.Wait(offset / rangeBlockSize) })
	}
	w.f.progress.setStage("range_download")
	close(w.f.streaming)
	w.initOnce.Do(func() { close(w.initialized) })
}

// The initial request is itself a scheduled block. A 200 fallback keeps that
// one execution until body close; a 206 exposes the spool before reading its block.
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
	// Each upstream attempt owns its timeout; intentional scheduler waits and
	// retries do not consume a shared execution timer. Parent cancellation and
	// the demand/task lifetime still bound all attempts.
	// The global block admission covers retries/redirects. HTTP attempts only
	// track their lifecycle; no lower-level hard quota delays blocked reads.

	var initial *http.Response
	if w.f.ranges == nil || w.f.ranges.sequential {
		resp, host, err := w.s.openUpstream(ctx, w.v, block, new(resourceAttempts))
		if err != nil {
			if ctx.Err() != nil {
				return blocksched.Result{Err: context.Cause(ctx)}
			}
			return blocksched.Result{Err: fmt.Errorf("%w: %w", errUpstreamDownload, err)}
		}
		defer resp.Body.Close()
		if resp.Request != nil && resp.Request.URL != nil {
			w.f.progress.setHost(resp.Request.URL.Hostname())
		}
		w.f.log.Info("upstream_response", "host", host, "status", resp.StatusCode, "content_length", resp.ContentLength)
		if resp.StatusCode != http.StatusPartialContent {
			w.s.mu.Lock()
			w.f.sequentialCtx = ctx
			w.s.mu.Unlock()
			w.f.sequentialDemand = func(int64) func() { return w.reads.Wait(block) }
			w.f.log.Info("range_unavailable", "host", host, "fallback", "sequential")
			stopBody := context.AfterFunc(ctx, func() { resp.Body.Close() })
			defer stopBody()
			if w.f.discard {
				_, err := io.Copy(io.Discard, contextReader{ctx, progressReader{upstreamReader{resp.Body}, w.f.progress}})
				return blocksched.Result{Err: err}
			}
			err := w.s.readSequential(ctx, resp.Body, w.v, w.f)
			// Sequential bodies have no safe partial restart. End this flight;
			// its next owner opens a new body after cleanup. Preserve storage
			// and integrity errors that happen to coincide with cancellation.
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				w.s.preemptFlight(w.f, nil)
				err = errPreempted
			}
			return blocksched.Result{Err: err}
		}
		v := w.v
		if resp.Request != nil {
			selected, err := w.s.parseResolved(resp.Request)
			if err != nil {
				return blocksched.Result{Err: err}
			}
			v.host, v.path, v.query = selected.host, selected.path, selected.query
		}
		var d *localResource
		if w.f.discard {
			d = &localResource{video: resourceVideo(v), etag: resp.Header.Get("ETag"),
				done: make(map[int64]bool)}
		} else if w.f.ranges != nil {
			d = w.f.ranges
			d.sequential = false
			d.etag = resp.Header.Get("ETag")
		} else {
			var err error
			d, err = w.s.newLocalResource(v, resp, w.f.log)
			if err != nil {
				return blocksched.Result{Err: err}
			}
		}
		w.initialize(d)
		initial = resp
	}
	err := w.s.readScheduledRange(ctx, block, initial, w.f, p)
	return blocksched.Result{Err: err}
}

func (w *downloadSession) submit(block int64, j *scheduledBlock, n blocksched.Need) error {
	key := w.reads.Key(block)
	remaining := min(rangeBlockSize, w.v.size-block*rangeBlockSize)
	t, d, err := w.s.scheduler.Schedule(key, remaining, func(ctx context.Context, p *blocksched.Progress) blocksched.Result { return w.run(ctx, p, block) }, n)
	if err != nil {
		if errors.Is(err, blocksched.ErrExpired) {
			w.s.expireDownload(w.f, w.v.key, block)
		}
		return err
	}
	t.Watch(w.wake)
	j.task, j.merged, j.published = t, d, n
	w.jobs[block] = j
	w.logDemand(block, j)
	return nil
}

func (w *downloadSession) plan(now time.Time, initialized bool) (map[int64]blockUse, <-chan struct{}, bool) {
	w.refetchAt = time.Time{}
	blocks := blockCount(w.v.size)
	w.s.mu.Lock()
	plan, active := w.s.usePlanLocked(w.v.key, now)
	if !initialized {
		// Malformed Range still needs response metadata before ServeContent rejects it.
		for d := range w.s.demands {
			if d.key == w.v.key && d.playback && d.clock == nil {
				plan[0] = mergeUse(plan[0], blockUse{need: blocksched.Need{Owner: w.v.key, LeaseUntil: now.Add(sourceLease), Opportunistic: true}, nextExpiry: now.Add(sourceLease)})
			}
		}
	}
	changed := w.s.capacityChanged
	w.s.mu.Unlock()
	requested := w.reads.Requested()
	for block := range requested {
		if _, ok := plan[block]; !ok {
			plan[block] = blockUse{} // The scheduler owns the independent read request.
		}
	}
	var local *localResource
	if initialized {
		local = w.f.ranges // Initialization publishes the executor's resource view.
	} else {
		// Before initialization, the executor may still be assigning f.ranges.
		// Reuse engine-owned availability without reading that unpublished field.
		w.s.retentionMu.Lock()
		local = w.s.localResources[w.v.key]
		w.s.retentionMu.Unlock()
	}
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
	if !initialized && len(plan) > 0 {
		initial := blocks
		for b := range plan {
			initial = min(initial, b)
		}
		for b := range w.jobs {
			initial = b
			break
		}
		var merged blockUse
		for _, use := range plan {
			merged = mergeUse(merged, use)
		}
		plan = map[int64]blockUse{initial: merged}
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

// reapCompleted distinguishes a complete sequential body from a completed range.
func (w *downloadSession) reapCompleted(initialized *bool) (bool, error) {
	for block, j := range w.jobs {
		if j.task == nil {
			continue
		}
		select {
		case <-j.task.Done():
			if errors.Is(j.task.Err(), blocksched.ErrExpired) {
				w.s.expireDownload(w.f, w.v.key, block)
				return false, j.task.Err()
			}
			if errors.Is(j.task.Err(), blocksched.ErrDeferred) {
				j.task, j.merged = nil, nil
				continue
			}
			// Lease expiry can finish before the coordinator marks retirement.
			// The scheduler's plain cancellation is still a stopped old task;
			// transport failures carry their own upstream error marker.
			j.retiring = j.retiring || j.task.Err() == context.Canceled
			// A stopped sequential body has already exposed its spool. Only
			// Range work can be recreated within this session. Done publishes
			// the executor's sequentialCtx before we inspect it here.
			if j.retiring && errors.Is(j.task.Err(), context.Canceled) && w.f.sequentialCtx != nil {
				return false, errPreempted
			}
			if err := j.task.Err(); err != nil && !(j.retiring && errors.Is(err, context.Canceled)) {
				return false, err
			}
			delete(w.jobs, block)
			// Headers and the first range can finish after the loop's snapshot.
			// Observe initialization again after Done before deciding that a
			// successful task downloaded the entire sequential response.
			select {
			case <-w.initialized:
				*initialized = true
			default:
			}
			if !*initialized && !j.retiring {
				return true, nil
			} // sequential 200, ready for independent local publication
			if j.task.Err() == nil && !w.f.discard && w.s.aggressiveBlockReclamation() {
				if w.finished == nil {
					w.finished = make(map[int64]bool)
				}
				w.finished[block] = true
			}
		default:
		}
	}
	return false, nil
}

func (w *downloadSession) close(result error) error {
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

func (s *Server) publishScheduled(ctx context.Context, path string, v video, f *flight) (result error) {
	w := newDownloadSession(s, ctx, v, f)
	defer func() { result = w.close(result) }()
	initialized := f.ranges != nil && (!f.ranges.sequential || f.ranges.complete())
	if initialized {
		w.initialize(f.ranges)
	}
	timer := time.NewTimer(sourceLease / 3)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		select {
		case <-w.initialized:
			initialized = true
		default:
		}
		whole, err := w.reapCompleted(&initialized)
		if err != nil {
			return err
		}
		if whole {
			break
		}
		now := time.Now()
		plan, changed, active := w.plan(now, initialized)
		// Planning filters completed blocks, so an empty plan can mean the
		// last range just finished. Check completion after that filtering and
		// before withdrawing demands; drain task results before hashing.
		select {
		case <-w.initialized:
			initialized = true
		default:
		}
		complete := false
		if initialized {
			complete = f.ranges.complete()
			if !complete && !f.discard && s.aggressiveBlockReclamation() {
				complete = len(plan) == 0 && w.refetchAt.IsZero() && w.allFetched()
				if complete && len(w.jobs) == 0 && !s.preemptFlight(f, changed) {
					continue // A new source invalidated completion based on old work.
				}
			}
			if complete && len(w.jobs) == 0 {
				break
			}
		}
		// A reader can accept the last bytes before the executor reports Done.
		// Drain that execution and its publication instead of treating delivery
		// as abandonment. This also covers sequential bodies awaiting EOF.
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
		until, retry, err := w.reconcile(plan, now, changed, active)
		if err != nil {
			return err
		}
		if retry {
			continue
		}
		if !active && len(plan) == 0 && !until.After(now) {
			return errPreempted
		}
		wait := sourceLease / 3
		if w.refetchAt.After(now) {
			wait = min(wait, w.refetchAt.Sub(now))
		}
		if !active && len(plan) == 0 {
			wait = min(wait, until.Sub(now))
		}
		for _, j := range w.jobs {
			if j.nextExpiry.After(now) {
				wait = min(wait, j.nextExpiry.Sub(now))
			}
		}
		timer.Reset(max(time.Millisecond, wait))
		var init <-chan struct{}
		if !initialized {
			init = w.initialized
		}
		select {
		case <-ctx.Done():
		case <-w.wake:
		case <-changed:
		case <-w.capacity:
		case <-timer.C:
		case <-init:
		}
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

// Historical completion only permits exit when plan has no remaining downloads
// or future refetch window. It never establishes current local availability.
func (w *downloadSession) allFetched() bool {
	for block := int64(0); block < blockCount(w.v.size); block++ {
		if !w.finished[block] && !w.f.ranges.has(block) {
			return false
		}
	}
	return true
}
