package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"still-wanna-dance/internal/blocksched"
)

const scheduledWindow = 8
const rangeReadAheadBlocks int64 = 2
const sourceLease = 30 * time.Second
const getHandoff = 2 * time.Second
const queueHandoff = 20 * time.Second

type scheduledCursor struct {
	ctx   context.Context
	exit  sourceExit
	block int64
	at    time.Time
}
type scheduledWait struct {
	block int64
	at    time.Time
}
type blockSource struct {
	kind string
	id   any
}
type plannedNeed struct {
	need     blocksched.Need
	revision int64
	source   demandSource
}
type scheduledBlock struct {
	task         *blocksched.Task
	needs        map[blockSource]plannedNeed
	handoffs     []handoffContribution
	merged       *blocksched.Demand
	published    blocksched.Need
	nextExpiry   time.Time
	handoffUntil time.Time
	retiring     bool
}
type downloadSession struct {
	s           *Server
	ctx         context.Context
	v           video
	f           *flight
	path        string
	started     time.Time
	wake        chan struct{}
	initialized chan struct{}
	initOnce    sync.Once
	opening     *resourceAttempts // first executor's state until headers are accepted
	mu          sync.Mutex
	cursors     map[*scheduledCursor]bool
	waits       map[*scheduledWait]bool
	jobs        map[int64]*scheduledBlock // resource coordinator only
}

func (w *downloadSession) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *downloadSession) track(ctx context.Context) (func(int64), func()) {
	c := &scheduledCursor{ctx: ctx, block: -1}
	w.mu.Lock()
	w.cursors[c] = true
	w.mu.Unlock()
	release := sync.OnceFunc(func() {
		c.exit.record()
		w.mu.Lock()
		delete(w.cursors, c)
		w.mu.Unlock()
		w.signal()
	})
	stop := context.AfterFunc(ctx, release)
	return func(offset int64) {
		w.mu.Lock()
		changed := false
		if c.exit.active(ctx) && c.block != offset/rangeBlockSize {
			c.block = offset / rangeBlockSize
			c.at = time.Now()
			changed = true
		}
		w.mu.Unlock()
		if changed {
			w.signal()
		}
	}, func() { stop(); release() }
}
func (w *downloadSession) demand(offset int64) func() {
	d := &scheduledWait{block: offset / rangeBlockSize, at: time.Now()}
	w.mu.Lock()
	w.waits[d] = true
	w.mu.Unlock()
	w.signal()
	return sync.OnceFunc(func() { w.mu.Lock(); delete(w.waits, d); w.mu.Unlock(); w.signal() })
}

func (w *downloadSession) initialize(d *rangeDownload) {
	w.f.ranges = d
	w.f.spool = d.makeSpool(w.demand, w.track)
	w.f.progress.setStage("range_download")
	close(w.f.streaming)
	w.initOnce.Do(func() { close(w.initialized) })
}

// The initial request is itself a scheduled block. A 200 fallback keeps that
// one execution until body close; a 206 exposes the spool before reading block 0.
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
	// The global block admission covers retries/redirects. HTTP attempts only
	// track their lifecycle; no lower-level hard quota delays blocked reads.

	var initial *http.Response
	if w.f.ranges == nil {
		if w.opening == nil {
			w.opening = new(resourceAttempts)
		}
		resp, host, err := w.s.openUpstream(ctx, w.v, w.opening)
		if err != nil {
			if isScheduledPause(ctx) {
				if r := w.opening.resume(); r != nil {
					// Keep the validated endpoint/cursor, not a canceled run's
					// context. The next execution supplies its own deadline.
					w.opening.request = r.Clone(context.Background())
				}
				return blocksched.Result{Err: blocksched.ErrPaused, Remaining: min(rangeBlockSize, w.v.size)}
			}
			return blocksched.Result{Err: fmt.Errorf("%w: %w", errUpstreamDownload, err)}
		}
		w.opening = nil // The response body now owns the cursor, including block 0.
		defer resp.Body.Close()
		if resp.Request != nil && resp.Request.URL != nil {
			w.f.progress.setHost(resp.Request.URL.Hostname())
		}
		w.f.log.Info("upstream_response", "host", host, "status", resp.StatusCode, "content_length", resp.ContentLength)
		if resp.StatusCode != http.StatusPartialContent {
			p.SetSize(w.v.size)
			w.f.sequentialDemand = func(int64) func() { return w.demand(0) }
			w.f.log.Info("range_unavailable", "host", host, "fallback", "sequential")
			stopBody := context.AfterFunc(ctx, func() { resp.Body.Close() })
			defer stopBody()
			err := w.s.publish(ctx, schedulerProgressReader{resp.Body, p}, w.path, w.v, w.f)
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
		d, err := w.s.newRangeDownload(v, resp, w.f.log)
		if err != nil {
			return blocksched.Result{Err: err}
		}
		w.initialize(d)
		initial = resp
	}
	err := w.s.readScheduledRange(ctx, block, initial, w.f, p)
	remaining := min(rangeBlockSize, w.v.size-block*rangeBlockSize)
	w.f.ranges.mu.Lock()
	if state := w.f.ranges.partial[block]; state != nil {
		remaining -= state.received
	}
	w.f.ranges.mu.Unlock()
	return blocksched.Result{Err: err, Remaining: remaining}
}

func (w *downloadSession) submit(block int64) (*scheduledBlock, error) {
	key := fmt.Sprintf("%s/%d/%d", w.v.key, w.v.size, block)
	remaining := min(rangeBlockSize, w.v.size-block*rangeBlockSize)
	if w.f.ranges != nil {
		w.f.ranges.mu.Lock()
		if state := w.f.ranges.partial[block]; state != nil {
			remaining -= state.received
		}
		w.f.ranges.mu.Unlock()
	}
	t, err := w.s.scheduler.Submit(key, remaining, func(ctx context.Context, p *blocksched.Progress) blocksched.Result { return w.run(ctx, p, block) })
	if err != nil {
		return nil, errors.Join(errBusy, err)
	}
	t.Watch(w.wake)
	j := &scheduledBlock{task: t, needs: make(map[blockSource]plannedNeed)}
	w.jobs[block] = j
	return j, nil
}

func (w *downloadSession) plan(now time.Time, initialized bool) (map[int64]map[blockSource]plannedNeed, <-chan struct{}) {
	plan := make(map[int64]map[blockSource]plannedNeed)
	blocks := (w.v.size + rangeBlockSize - 1) / rangeBlockSize
	done := func(block int64) bool {
		if !initialized {
			return false
		}
		w.f.ranges.mu.Lock()
		defer w.f.ranges.mu.Unlock()
		return w.f.ranges.done[block]
	}
	first := int64(0)
	if initialized {
		w.f.ranges.mu.Lock()
		first = w.f.ranges.prefix
		w.f.ranges.mu.Unlock()
	}
	add := func(block int64, source blockSource, ready time.Time, blocking bool, revision int64, origin demandSource) {
		if block < 0 || block >= blocks || done(block) {
			return
		}
		if plan[block] == nil {
			plan[block] = make(map[blockSource]plannedNeed)
		}
		plan[block][source] = plannedNeed{need: blocksched.Need{Owner: w.v.key, ReadyBy: ready, LeaseUntil: now.Add(sourceLease), Blocking: blocking}, revision: revision, source: origin}
	}
	var window []int64
	for b := first; b < blocks && len(window) < scheduledWindow; b++ {
		if !done(b) {
			window = append(window, b)
		}
	}
	w.s.mu.Lock()
	changed := w.s.capacityChanged
	for d := range w.s.demands {
		if d.key != w.v.key || !d.exit.active(d.ctx) || d.queue {
			continue
		}
		kind := "batch"
		if d.playback {
			kind = "get"
		}
		for _, b := range window {
			ready := time.Time{}
			if d.playback {
				ready = w.started.Add(time.Minute + time.Duration(b)*2*time.Second)
				if !initialized {
					ready = w.started.Add(time.Second)
				}
			}
			origin := sourceBatch
			if d.playback {
				origin = sourceGET
			}
			add(b, blockSource{kind, d}, ready, false, 0, origin)
		}
	}
	for song, key := range w.s.prefetchResources {
		if key != w.v.key {
			continue
		}
		if source := w.s.prefetchSources[song]; source != nil {
			for _, b := range window {
				add(b, blockSource{"queue", source}, source.readyBy(b), false, int64(source.position), source.origin())
			}
		}
	}
	w.s.mu.Unlock()
	w.mu.Lock()
	for c := range w.cursors {
		if !c.exit.active(c.ctx) || c.block < 0 {
			continue
		}
		for distance := int64(0); distance <= rangeReadAheadBlocks; distance++ {
			add(c.block+distance, blockSource{"read", c}, c.at.Add(time.Duration(distance+1)*2*time.Second), false, c.block, sourceGET)
		}
	}
	for d := range w.waits {
		add(d.block, blockSource{"blocking", d}, d.at, true, 0, sourceGET)
	}
	w.mu.Unlock()
	// Before headers, only block 0 can establish Range capability and entity.
	if !initialized {
		for b := range plan {
			if b != 0 {
				delete(plan, b)
			}
		}
	}
	return plan, changed
}

// All source transitions for one block are merged before a single scheduler
// update. The scheduler never observes the intermediate GET/handoff ordering.
func (w *downloadSession) reconcile(plan map[int64]map[blockSource]plannedNeed, now time.Time, changed <-chan struct{}) (time.Time, bool, error) {
	var until time.Time
	order := make([]int64, 0, len(plan))
	for block := range plan {
		order = append(order, block)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, block := range order {
		j := w.jobs[block]
		if j == nil {
			var err error
			j, err = w.submit(block)
			if err != nil {
				return until, false, err
			}
		}
		if j.retiring {
			continue
		}
		for source, p := range plan[block] {
			if old, ok := j.needs[source]; ok && old.revision == p.revision && old.source == p.source {
				// Queue deadlines already use a stable, shared epoch. Even an
				// unchanged position must follow that epoch after a reorder.
				if source.kind != "queue" {
					p.need.ReadyBy = old.need.ReadyBy
				}
				if old.need.LeaseUntil.Sub(now) > sourceLease/2 {
					p.need.LeaseUntil = old.need.LeaseUntil
				}
			}
			j.needs[source] = p
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
		for source, old := range j.needs {
			if _, ok := plan[block][source]; ok {
				continue
			}
			delete(j.needs, source)
			reason := w.withdrawal(block, source)
			at := now
			switch record := source.id.(type) {
			case *downloadDemand:
				if exited := record.exit.when(); !exited.IsZero() {
					at = exited
				}
			case *scheduledCursor:
				if exited := record.exit.when(); !exited.IsZero() {
					at = exited
				}
			case *queueSource:
				w.s.mu.Lock()
				if !record.removedAt.IsZero() {
					old.source, old.need.ReadyBy, at = record.origin(), record.readyBy(block), record.removedAt
				}
				w.s.mu.Unlock()
			}
			if h, ok := makeHandoff(old, reason, at); ok {
				j.handoffs = append(j.handoffs, h)
				if w.f.log != nil {
					w.f.log.Debug("block_demand_handoff", "block", block, "from", old.source.String(), "reason", "source_gone", "withdrawn_at", at, "ready_by", h.need.ReadyBy, "lease_until", h.need.LeaseUntil)
				}
			}
		}
		n := j.aggregate(now)
		if len(j.needs)+len(j.handoffs) > maxBlockContributions {
			return until, false, errors.Join(errBusy, blocksched.ErrCapacity)
		}
		until = maxTime(until, n.LeaseUntil)
		updates[block] = n
	}
	// Stop accepting readers before canceling the last tasks or waiting for
	// their bodies/checkpoints to close. A concurrent GET invalidates an idle
	// snapshot and must be planned before any irreversible task retirement.
	if len(plan) == 0 && !until.After(now) && !w.s.preemptFlight(w.f, changed) {
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
			j.task.Close()
			continue
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

func (w *downloadSession) withdrawal(block int64, source blockSource) withdrawalReason {
	if w.ctx.Err() != nil {
		return withdrawalShutdown
	}
	// The first executor publishes ranges before closing initialized. Do not
	// read that pointer concurrently with first-response initialization.
	select {
	case <-w.initialized:
		w.f.ranges.mu.Lock()
		done := w.f.ranges.done[block]
		w.f.ranges.mu.Unlock()
		if done {
			return withdrawalCompleted
		}
	default:
	}
	if source.kind == "blocking" {
		return withdrawalReadMoved
	}
	if source.kind == "read" {
		if c, ok := source.id.(*scheduledCursor); ok && c.exit.active(c.ctx) {
			return withdrawalReadMoved
		}
	}
	return withdrawalSourceGone
}
func handoffNeed(n blocksched.Need, now time.Time, grace, delay time.Duration) blocksched.Need {
	n.Blocking = false
	n.LeaseUntil = now.Add(grace)
	n.ReadyBy = maxTime(n.ReadyBy, now).Add(delay)
	return n
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
		select {
		case <-j.task.Done():
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
		j.task.Close()
	}
	for _, j := range w.jobs {
		result = preferDownloadError(result, j.task.Err())
	}
	return result
}

func (s *Server) publishScheduled(ctx context.Context, path string, v video, f *flight) (result error) {
	w := &downloadSession{s: s, ctx: ctx, v: v, f: f, path: path, started: time.Now(), wake: make(chan struct{}, 1), initialized: make(chan struct{}), cursors: make(map[*scheduledCursor]bool), waits: make(map[*scheduledWait]bool), jobs: make(map[int64]*scheduledBlock)}
	defer func() { result = w.close(result) }()
	initialized := f.ranges != nil
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
		plan, changed := w.plan(now, initialized)
		// Planning filters completed blocks, so an empty plan can mean the
		// last range just finished. Check completion after that filtering and
		// before withdrawing demands; drain task results before hashing.
		select {
		case <-w.initialized:
			initialized = true
		default:
		}
		if initialized {
			f.ranges.mu.Lock()
			complete := int64(len(f.ranges.done)) == (v.size+rangeBlockSize-1)/rangeBlockSize
			f.ranges.mu.Unlock()
			if complete && len(w.jobs) == 0 {
				break
			}
			if complete {
				select {
				case <-ctx.Done():
				case <-w.wake:
				}
				continue
			}
		}
		until, retry, err := w.reconcile(plan, now, changed)
		if err != nil {
			return err
		}
		if retry {
			continue
		}
		if len(plan) == 0 && !until.After(now) {
			return errPreempted
		}
		wait := sourceLease / 3
		if len(plan) == 0 {
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
		case <-timer.C:
		case <-init:
		}
	}
	f.progress.setStage("hash")
	if err := s.checkDownloadedFile(ctx, f.ranges.storage.file, v); err != nil {
		return err
	}
	f.log.Info("download_verified", "bytes", v.size, "size_ok", true, "checksum_ok", true)
	return s.publishSpool(ctx, f.ranges.storage.file, path, v, f)
}
