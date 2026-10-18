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

type scheduledWait struct {
	block int64
	at    time.Time
}
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
	path        string
	wake        chan struct{}
	initialized chan struct{}
	initOnce    sync.Once
	opening     *resourceAttempts // first executor's state until headers are accepted
	mu          sync.Mutex
	waits       map[*scheduledWait]bool
	jobs        map[int64]*scheduledBlock // resource coordinator only
	capacity    <-chan struct{}
}

func (w *downloadSession) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *downloadSession) demand(offset int64) func() {
	d := &scheduledWait{block: offset / rangeBlockSize, at: time.Now()}
	w.mu.Lock()
	w.waits[d] = true
	w.mu.Unlock()
	w.signal()
	return sync.OnceFunc(func() { w.mu.Lock(); delete(w.waits, d); w.mu.Unlock(); w.signal() })
}

func (w *downloadSession) initialize(d *localResource) {
	w.f.ranges = d
	if !w.f.discard {
		w.f.spool = d.makeSpool(w.demand)
	}
	w.f.progress.setStage("range_download")
	close(w.f.streaming)
	w.initOnce.Do(func() { close(w.initialized) })
}

// The initial request is itself a scheduled block. A 200 fallback keeps that
// one execution until body close; a 206 exposes the spool before reading its block.
func (w *downloadSession) run(ctx context.Context, p *blocksched.Progress, block int64) (result blocksched.Result) {
	if w.ctx.Err() != nil {
		return blocksched.Result{Err: context.Cause(w.ctx)}
	}
	defer func() {
		// A withdrawn block can resume in this session, but a canceled session
		// cannot. Its checkpoint belongs to the next flight after cleanup.
		if errors.Is(result.Err, blocksched.ErrPaused) && w.ctx.Err() != nil {
			result.Err = context.Cause(w.ctx)
		}
	}()
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
		if w.opening == nil {
			w.opening = new(resourceAttempts)
		}
		resp, host, err := w.s.openUpstream(ctx, w.v, block, w.opening)
		if err != nil {
			if isScheduledPause(ctx) {
				if r := w.opening.resume(); r != nil {
					// Keep the validated endpoint/cursor, not a canceled run's
					// context. The next execution supplies its own deadline.
					w.opening.request = r.Clone(context.Background())
				}
				return blocksched.Result{Err: blocksched.ErrPaused, Remaining: min(rangeBlockSize, w.v.size-block*rangeBlockSize)}
			}
			return blocksched.Result{Err: fmt.Errorf("%w: %w", errUpstreamDownload, err)}
		}
		w.opening = nil // The response body now owns the initial request's cursor.
		defer resp.Body.Close()
		if resp.Request != nil && resp.Request.URL != nil {
			w.f.progress.setHost(resp.Request.URL.Hostname())
		}
		w.f.log.Info("upstream_response", "host", host, "status", resp.StatusCode, "content_length", resp.ContentLength)
		if resp.StatusCode != http.StatusPartialContent {
			w.s.mu.Lock()
			w.f.sequentialCtx = ctx
			w.s.mu.Unlock()
			w.f.sequentialDemand = func(int64) func() { return w.demand(block * rangeBlockSize) }
			w.f.log.Info("range_unavailable", "host", host, "fallback", "sequential")
			stopBody := context.AfterFunc(ctx, func() { resp.Body.Close() })
			defer stopBody()
			if w.f.discard {
				_, err := io.Copy(io.Discard, contextReader{ctx, progressReader{upstreamReader{resp.Body}, w.f.progress}})
				return blocksched.Result{Err: err}
			}
			err := w.s.publish(ctx, resp.Body, w.path, w.v, w.f)
			// Sequential bodies have no safe partial restart. End this flight;
			// its next owner opens a new body after cleanup. Preserve storage
			// and integrity errors that happen to coincide with cancellation.
			if isScheduledPause(ctx) && (errors.Is(err, context.Canceled) || errors.Is(err, blocksched.ErrPaused)) {
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
	remaining := min(rangeBlockSize, w.v.size-block*rangeBlockSize)
	return blocksched.Result{Err: err, Remaining: remaining}
}

func (w *downloadSession) submit(block int64, j *scheduledBlock, n blocksched.Need) error {
	key := fmt.Sprintf("%s/%d/%d", w.v.key, w.v.size, block)
	remaining := min(rangeBlockSize, w.v.size-block*rangeBlockSize)
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

func (w *downloadSession) plan(now time.Time, initialized bool) (map[int64]blockUse, <-chan struct{}, bool) {
	blocks := (w.v.size + rangeBlockSize - 1) / rangeBlockSize
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
	w.mu.Lock()
	for d := range w.waits {
		plan[d.block] = mergeUse(plan[d.block], blockUse{need: blocksched.Need{Owner: w.v.key, ReadyBy: d.at, LeaseUntil: now.Add(sourceLease), Blocking: true}, nextExpiry: now.Add(sourceLease)})
	}
	w.mu.Unlock()
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
	for b := range plan {
		if b < 0 || b >= blocks || local != nil && local.has(b) {
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
	// Register actual blocked reads before ordinary blocks of this resource.
	sort.Slice(order, func(i, j int) bool {
		if a, b := plan[order[i]].need.Blocking, plan[order[j]].need.Blocking; a != b {
			return a
		}
		return order[i] < order[j]
	})
	for _, block := range order {
		j := w.jobs[block]
		if j == nil {
			j = &scheduledBlock{}
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
	// their bodies/checkpoints to close. A concurrent GET invalidates an idle
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
		if !n.LeaseUntil.After(now) {
			j.retiring = true
			if j.task != nil {
				j.task.Close()
			} else {
				delete(w.jobs, block)
			}
			continue
		}
		if j.task == nil {
			if err := w.submit(block, j, n); err != nil {
				if errors.Is(err, blocksched.ErrCapacity) {
					w.capacity = capacity
					continue
				}
				return until, false, err
			}
		}
		if j.merged != nil && j.published == n {
			continue
		}
		if j.merged == nil || !j.merged.Update(n) {
			j.merged = j.task.Add(n)
			if !j.merged.Accepted() {
				select {
				case <-j.task.Done():
					continue
				default:
					return until, false, errors.Join(errBusy, blocksched.ErrCapacity)
				}
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

// reapCompleted distinguishes a published sequential body from a completed range.
func (w *downloadSession) reapCompleted(initialized *bool) (bool, error) {
	for block, j := range w.jobs {
		if j.task == nil {
			continue
		}
		select {
		case <-j.task.Done():
			if errors.Is(j.task.Err(), blocksched.ErrDeferred) {
				j.task, j.merged = nil, nil
				continue
			}
			if err := j.task.Err(); err != nil && !(j.retiring && errors.Is(err, context.Canceled)) {
				return false, err
			}
			delete(w.jobs, block)
			// Headers and the first range can finish after the loop's snapshot.
			// Observe initialization again after Done before deciding that a
			// successful task published the entire sequential response.
			select {
			case <-w.initialized:
				*initialized = true
			default:
			}
			if !*initialized && !j.retiring {
				return true, nil
			} // sequential 200, already published
		default:
		}
	}
	return false, nil
}

func (w *downloadSession) close(result error) error {
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
	w := &downloadSession{s: s, ctx: ctx, v: v, f: f, path: path, wake: make(chan struct{}, 1), initialized: make(chan struct{}), waits: make(map[*scheduledWait]bool), jobs: make(map[int64]*scheduledBlock)}
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
		if published, err := w.reapCompleted(&initialized); err != nil || published {
			return err
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
