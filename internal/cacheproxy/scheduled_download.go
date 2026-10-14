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

const scheduledWindow = 8
const rangeReadAheadBlocks int64 = 2
const sourceLease = 30 * time.Second
const getHandoff = 2 * time.Second
const queueHandoff = 20 * time.Second

type scheduledCursor struct {
	ctx   context.Context
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
}
type attachedNeed struct {
	handle *blocksched.Demand
	plan   plannedNeed
}
type scheduledBlock struct {
	task         *blocksched.Task
	needs        map[blockSource]attachedNeed
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
func (w *downloadSession) track(ctx context.Context) func(int64) {
	c := &scheduledCursor{ctx: ctx, block: -1}
	w.mu.Lock()
	w.cursors[c] = true
	w.mu.Unlock()
	context.AfterFunc(ctx, func() { w.mu.Lock(); delete(w.cursors, c); w.mu.Unlock(); w.signal() })
	return func(offset int64) {
		w.mu.Lock()
		changed := false
		if ctx.Err() == nil && c.block != offset/rangeBlockSize {
			c.block = offset / rangeBlockSize
			c.at = time.Now()
			changed = true
		}
		w.mu.Unlock()
		if changed {
			w.signal()
		}
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

func (w *downloadSession) initialize(d *rangeDownload) {
	w.f.ranges = d
	w.f.spool = d.makeSpool(w.demand, w.track)
	w.f.progress.setStage("range_download")
	close(w.f.streaming)
	w.initOnce.Do(func() { close(w.initialized) })
}

// The initial request is itself a scheduled block. A 200 fallback keeps that
// one execution until body close; a 206 exposes the spool before reading block 0.
func (w *downloadSession) run(ctx context.Context, p *blocksched.Progress, block int64) blocksched.Result {
	if deadline, ok := w.ctx.Deadline(); ok {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, deadline)
		defer stop()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(w.ctx, func() { cancel(context.Cause(w.ctx)) })
	defer stop()
	defer cancel(nil)
	// The global block permit already bounds all retries/redirects. The HTTP
	// guard remains a final lifecycle check, with no second priority policy.

	var initial *http.Response
	if w.f.ranges == nil {
		resp, host, err := w.s.openUpstream(ctx, w.v)
		if err != nil {
			if isScheduledPause(ctx) {
				return blocksched.Result{Err: blocksched.ErrPaused, Remaining: min(rangeBlockSize, w.v.size)}
			}
			return blocksched.Result{Err: fmt.Errorf("%w: %w", errUpstreamDownload, err)}
		}
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
			// its next owner opens a new body after cleanup.
			if err != nil && isScheduledPause(ctx) {
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
	j := &scheduledBlock{task: t, needs: make(map[blockSource]attachedNeed)}
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
	add := func(block int64, source blockSource, ready time.Time, blocking bool, revision int64) {
		if block < 0 || block >= blocks || done(block) {
			return
		}
		if plan[block] == nil {
			plan[block] = make(map[blockSource]plannedNeed)
		}
		plan[block][source] = plannedNeed{blocksched.Need{Owner: w.v.key, ReadyBy: ready, LeaseUntil: now.Add(sourceLease), Blocking: blocking}, revision}
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
		if d.key != w.v.key || d.ctx.Err() != nil || d.queue {
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
			add(b, blockSource{kind, d}, ready, false, 0)
		}
	}
	for song, key := range w.s.prefetchResources {
		if key != w.v.key {
			continue
		}
		if position, ok := w.s.prefetchOrder[song]; ok {
			for _, b := range window {
				add(b, blockSource{"queue", song}, now.Add(10*time.Second+time.Duration(position)*30*time.Second+time.Duration(b)*2*time.Second), false, int64(position))
			}
		}
	}
	w.s.mu.Unlock()
	w.mu.Lock()
	for c := range w.cursors {
		if c.ctx.Err() != nil || c.block < 0 {
			continue
		}
		for distance := int64(0); distance <= rangeReadAheadBlocks; distance++ {
			add(c.block+distance, blockSource{"read", c}, c.at.Add(time.Duration(distance+1)*2*time.Second), false, c.block)
		}
	}
	for d := range w.waits {
		add(d.block, blockSource{"blocking", d}, d.at, true, 0)
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

func (w *downloadSession) reconcile(plan map[int64]map[blockSource]plannedNeed, now time.Time) (time.Time, error) {
	var until time.Time
	order := make([]int64, 0, len(plan))
	for block := range plan {
		order = append(order, block)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, block := range order {
		needs := plan[block]
		j := w.jobs[block]
		if j == nil {
			var err error
			j, err = w.submit(block)
			if err != nil {
				return until, err
			}
		}
		if j.retiring {
			continue
		}
		for source, p := range needs {
			if old, ok := j.needs[source]; ok {
				if old.plan.revision == p.revision {
					p.need.ReadyBy = old.plan.need.ReadyBy
				}
				if old.plan.need.LeaseUntil.Sub(now) > sourceLease/2 && old.plan.revision == p.revision {
					continue
				}
				if old.handle.Update(p.need) {
					j.needs[source] = attachedNeed{old.handle, p}
					continue
				}
			}
			handle := j.task.Add(p.need)
			if !handle.Accepted() {
				select {
				case <-j.task.Done():
					continue
				default:
					return until, errors.Join(errBusy, blocksched.ErrCapacity)
				}
			}
			j.needs[source] = attachedNeed{handle, p}
		}
	}
	for block, j := range w.jobs {
		for source, old := range j.needs {
			if _, ok := plan[block][source]; ok {
				continue
			}
			var handoff *blocksched.Need
			grace, delay := time.Duration(0), time.Duration(0)
			switch source.kind {
			case "get", "read":
				grace, delay = getHandoff, 2*time.Second
			case "queue":
				grace, delay = queueHandoff, 5*time.Second
			}
			if grace > 0 {
				n := handoffNeed(old.plan.need, now, grace, delay)
				handoff = &n
				j.handoffUntil = maxTime(j.handoffUntil, n.LeaseUntil)
			}
			old.handle.Close(handoff)
			delete(j.needs, source)
		}
		if len(j.needs) > 0 {
			until = maxTime(until, now.Add(sourceLease))
		}
		until = maxTime(until, j.handoffUntil)
		if len(j.needs) == 0 && !j.handoffUntil.After(now) && !j.retiring {
			j.retiring = true
			j.task.Close()
		}
	}
	return until, nil
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

func (s *Server) publishScheduled(ctx context.Context, path string, v video, f *flight) (result error) {
	w := &downloadSession{s: s, ctx: ctx, v: v, f: f, path: path, started: time.Now(), wake: make(chan struct{}, 1), initialized: make(chan struct{}), cursors: make(map[*scheduledCursor]bool), waits: make(map[*scheduledWait]bool), jobs: make(map[int64]*scheduledBlock)}
	defer func() {
		for _, j := range w.jobs {
			j.task.Close()
		}
		for _, j := range w.jobs {
			err := j.task.Err()
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errPreempted) && !errors.Is(err, blocksched.ErrClosed) {
				result = err
			}
		}
	}()
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
		for block, j := range w.jobs {
			select {
			case <-j.task.Done():
				if err := j.task.Err(); err != nil && !(j.retiring && errors.Is(err, context.Canceled)) {
					return err
				}
				delete(w.jobs, block)
				if !initialized && !j.retiring {
					return nil
				} // sequential 200, already published
			default:
			}
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
		now := time.Now()
		plan, changed := w.plan(now, initialized)
		until, err := w.reconcile(plan, now)
		if err != nil {
			return err
		}
		if len(plan) == 0 && !until.After(now) {
			return errPreempted
		}
		wait := sourceLease / 3
		if len(plan) == 0 {
			wait = min(wait, until.Sub(now))
		}
		for _, j := range w.jobs {
			if len(j.needs) == 0 && j.handoffUntil.After(now) {
				wait = min(wait, j.handoffUntil.Sub(now))
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
	if _, err := f.ranges.storage.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := checkOpenFile(ctx, f.ranges.storage.file, v); err != nil {
		return fmt.Errorf("%w: %w", errUpstreamDownload, err)
	}
	f.log.Info("download_verified", "bytes", v.size, "size_ok", true, "checksum_ok", true)
	return s.publishSpool(ctx, f.ranges.storage.file, path, v, f)
}
