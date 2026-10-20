// Package blocksched schedules opaque pieces of work. Callers own
// content identity and storage; the scheduler owns demand leases and execution.
package blocksched

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrClosed   = errors.New("block scheduler closed")
	ErrCapacity = errors.New("block scheduler metadata capacity reached")
	ErrDeferred = errors.New("queued block deferred for more urgent work")
	ErrExpired  = fmt.Errorf("download need-by window expired: %w", context.DeadlineExceeded)
)

// Need separates urgency from source validity. Finite work must end at
// ReadyBy + DownloadGrace even while its source lease remains valid.
// Owner is opaque and must be shared by duplicate/overlapping requests.
type Need struct {
	Owner               string
	ReadyBy, LeaseUntil time.Time
	// Opportunistic preserves an independent ordinary demand that can use
	// capacity before ReadyBy. It bypasses only the window, never the quota.
	Opportunistic bool
	// WindowClosed keeps a valid source without admitting new work from it.
	// Only a source update can reopen it; elapsed time cannot. Independent
	// open contributions and actual reads still qualify, and running work
	// keeps its normal lease/deadline lifecycle.
	WindowClosed bool
}

// priority is scheduler-owned. Callers can publish ordinary usage only;
// actual local read waits supply Blocking independently of those leases.
type priority struct {
	Need
	Blocking bool
}

type Result struct {
	Err error
}

// Run must close bodies and finish writes before returning. Cancellation ends
// this task; later work is scheduled anew. Queued tasks need no data buffer.
type Run func(context.Context, *Progress) Result

type Progress struct {
	// Written by the executor and read only after Run returns.
	admitted, ready time.Time
	work            int64
}

// Ready records when a checked block becomes readable. Call only for a block,
// not a sequential whole-resource fallback. Cleanup may continue after Ready;
// the active slot is held until Run returns. Calls belong to the Run goroutine.
func (p *Progress) Ready() {
	if p.ready.IsZero() {
		p.ready = time.Now()
	}
}

type Config struct {
	// SoftLimit bounds ordinary admission. Actual blocked reads bypass both
	// this limit and growth pacing; metadata capacity still bounds total work.
	SoftLimit, MaxTasks, MaxDemands int
	GrowInterval                    time.Duration
	// PrepareAhead is the full lead M in ReadyBy <= now + M; zero uses DefaultPrepareAhead.
	PrepareAhead time.Duration
	// Optional event sink; it must be fast and must not call back into Scheduler.
	Observe func(Event)
}
type Event struct {
	Kind, Key, Reason       string
	Active, Open            int
	Blocking, Opportunistic bool
	ReadyBy                 time.Time
	WorkBytes               int64
	BlockTime, PrepareAhead time.Duration
	Overdue                 bool
}

type Scheduler struct {
	commands chan func(*state)
	finished chan completion
	done     chan struct{}
	once     sync.Once
}

type Task struct {
	s    *Scheduler
	key  string
	done chan struct{}
	err  error // read only after Done
	wake chan<- struct{}
}

// Watch coalesces completion notifications for a resource coordinator. It does
// not create a waiting goroutine for every queued block.
func (t *Task) Watch(wake chan<- struct{}) {
	t.s.call(func(s *state) {
		t.wake = wake
		select {
		case <-t.done:
			notify(wake)
		default:
		}
	})
}
func notify(wake chan<- struct{}) {
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (t *Task) Done() <-chan struct{} { return t.done }
func (t *Task) Err() error            { <-t.done; return t.err }
func (t *Task) Close() {
	t.s.call(func(s *state) {
		if j := s.jobs[t.key]; j != nil && j.task == t {
			s.retire(j, context.Canceled)
		}
	})
}

type Demand struct {
	task *Task
	id   uint64
}

func (d *Demand) Accepted() bool { return d.id != 0 }

func (t *Task) Add(n Need) *Demand {
	d := &Demand{task: t}
	t.s.call(func(s *state) { d = s.add(t, n) })
	return d
}

func (s *state) add(t *Task, n Need) *Demand {
	d := &Demand{task: t}
	j := s.jobs[t.key]
	if j == nil || j.task != t || j.retiring || n.Owner == "" || s.demandCount >= s.cfg.MaxDemands || !n.LeaseUntil.After(time.Now()) {
		return d
	}
	s.sequence++
	d.id = s.sequence
	v := &lease{id: d.id, job: j, need: n, index: -1}
	j.needs[d.id] = v
	s.demandCount++
	heap.Push(&s.expiry, v)
	s.refresh(j)
	return d
}

// Update cannot recreate an expired, closed or replaced demand.
func (d *Demand) Update(n Need) bool {
	ok := false
	d.task.s.call(func(s *state) {
		if v := s.lookup(d); v != nil && n.Owner != "" && n.LeaseUntil.After(time.Now()) {
			v.need = n
			heap.Fix(&s.expiry, v.index)
			s.refresh(v.job)
			ok = true
		}
	})
	return ok
}

// Close withdraws this contribution. A late Update cannot recreate it.
func (d *Demand) Close() {
	d.task.s.call(func(s *state) {
		v := s.lookup(d)
		if v == nil {
			return
		}
		j := v.job
		s.removeLease(v)
		s.refresh(j)
	})
}

type job struct {
	task           *Task
	run            Run
	remaining      int64
	needs          map[uint64]*lease
	need           priority
	index          [2]int // one membership per deadline/opportunistic heap
	sequence, fair uint64
	cancel         context.CancelCauseFunc
	progress       *Progress
	retiring       bool
	terminal       error
	deadlineIndex  int
}
type lease struct {
	id    uint64
	job   *job
	need  Need
	index int
}
type completion struct {
	job    *job
	result Result
}
type state struct {
	s                   *Scheduler
	cfg                 Config
	jobs                map[string]*job
	reads               map[string]map[*readWait]bool
	ready               readyHeap
	untimed             readyHeap
	expiry              leaseHeap
	deadlines           deadlineHeap
	owners              map[string]uint64
	groups              map[string]map[*job]bool
	sequence            uint64
	active, demandCount int
	closing             bool
	control             controller
	capacityChanged     chan struct{}
	observers           map[chan<- struct{}]bool
}

func New(cfg Config) *Scheduler {
	cfg.SoftLimit = max(1, cfg.SoftLimit)
	if cfg.PrepareAhead <= 0 {
		cfg.PrepareAhead = DefaultPrepareAhead
	}
	if cfg.MaxTasks <= 0 {
		cfg.MaxTasks = 4096
	}
	if cfg.MaxDemands <= 0 {
		cfg.MaxDemands = 32768
	}
	s := &Scheduler{commands: make(chan func(*state), 64), finished: make(chan completion, cfg.SoftLimit), done: make(chan struct{})}
	st := &state{s: s, cfg: cfg, jobs: make(map[string]*job), reads: make(map[string]map[*readWait]bool), owners: make(map[string]uint64), groups: make(map[string]map[*job]bool), capacityChanged: make(chan struct{})}
	st.untimed.slot = 1
	st.control.open = cfg.SoftLimit
	if cfg.GrowInterval > 0 {
		st.control.open = 1
	}
	go st.loop()
	return s
}

func (s *Scheduler) call(fn func(*state)) bool {
	ack := make(chan struct{})
	select {
	case s.commands <- func(st *state) { fn(st); close(ack) }:
		select {
		case <-ack:
			return true
		case <-s.done:
			return false
		}
	case <-s.done:
		return false
	}
}

func (s *Scheduler) Close() {
	s.once.Do(func() {
		s.call(func(st *state) {
			st.closing = true
			st.capacityAvailable()
			for _, j := range st.jobs {
				st.retire(j, ErrClosed)
			}
		})
	})
	<-s.done
}

// SetSoftLimit changes only ordinary admission. Existing executions drain;
// raising the ceiling still uses the configured growth interval. Call outside
// Observe, which runs on the scheduler goroutine.
func (s *Scheduler) SetSoftLimit(limit int) bool {
	if limit < 1 {
		return false
	}
	return s.call(func(st *state) {
		st.cfg.SoftLimit = limit
		st.control.open = min(st.control.open, limit)
		if st.cfg.GrowInterval <= 0 {
			st.control.open = limit
		}
		st.control.nextGrow = time.Time{}
		st.emit("capacity", "limit_changed", nil)
	})
}
func (s *state) lookup(d *Demand) *lease {
	j := s.jobs[d.task.key]
	if j == nil || j.task != d.task || j.retiring {
		return nil
	}
	v := j.needs[d.id]
	if v == nil || !v.need.LeaseUntil.After(time.Now()) {
		return nil
	}
	return v
}
func (s *state) removeLease(v *lease) {
	heap.Remove(&s.expiry, v.index)
	delete(v.job.needs, v.id)
	s.demandCount--
	s.capacityAvailable()
}
func (s *state) refresh(j *job) {
	previous := j.need
	n := Need{}
	var earliest time.Time
	opportunistic := false
	for _, v := range j.needs {
		if !v.need.LeaseUntil.After(time.Now()) {
			continue
		}
		opportunistic = opportunistic || !v.need.WindowClosed && (v.need.Opportunistic || v.need.ReadyBy.IsZero())
		if n.Owner == "" || n.WindowClosed && !v.need.WindowClosed {
			earliest = v.need.ReadyBy
		} else if n.WindowClosed == v.need.WindowClosed {
			earliest = earlier(earliest, v.need.ReadyBy)
		}
		if n.Owner == "" || needBefore(priority{Need: v.need}, priority{Need: n}) || !needBefore(priority{Need: n}, priority{Need: v.need}) && v.need.Owner < n.Owner {
			n = v.need
		}
	}
	n.ReadyBy, n.Opportunistic = earliest, opportunistic
	p := priority{Need: n}
	if !j.retiring {
		p = s.withReads(j.task.key, n)
	}
	if j.need.Owner != p.Owner {
		s.ungroup(j)
	}
	j.need = p
	s.updateDeadline(j)
	if previous.Blocking != p.Blocking || previous.ReadyBy != p.ReadyBy || previous.Opportunistic != p.Opportunistic || previous.WindowClosed != p.WindowClosed {
		s.capacityAvailable()
	}
	if p.Owner != "" {
		if s.groups[p.Owner] == nil {
			s.groups[p.Owner] = make(map[*job]bool)
		}
		s.groups[p.Owner][j] = true
	}
	j.fair = s.owners[p.Owner]
	if j.retiring {
		s.unqueue(j)
		if j.cancel != nil {
			j.cancel(j.terminal)
		}
		return
	}
	if p.Owner == "" {
		s.emit("retired", "no_demand", j)
		s.retire(j, context.Canceled)
		return
	}
	if j.cancel != nil {
		return
	}
	wasQueued := j.index[0] >= 0 || j.index[1] >= 0
	s.ready.set(j, p.Blocking || !p.WindowClosed && !p.ReadyBy.IsZero())
	s.untimed.set(j, !p.Blocking && p.Opportunistic)
	if !wasQueued {
		s.emit("queued", "demand", j)
	}
}
func (s *state) ungroup(j *job) {
	owner := j.need.Owner
	if !s.groups[owner][j] {
		return
	}
	delete(s.groups[owner], j)
	if len(s.groups[owner]) == 0 {
		delete(s.groups, owner)
		delete(s.owners, owner)
	}
}
func (s *state) unqueue(j *job) {
	s.ready.set(j, false)
	s.untimed.set(j, false)
}
func needBefore(a, b priority) bool {
	if a.Blocking != b.Blocking {
		return a.Blocking
	}
	if a.Blocking {
		return false
	}
	if a.WindowClosed != b.WindowClosed {
		return !a.WindowClosed
	}
	if a.ReadyBy.IsZero() != b.ReadyBy.IsZero() {
		return !a.ReadyBy.IsZero()
	}
	return a.ReadyBy.Before(b.ReadyBy)
}
func (s *state) retire(j *job, err error) {
	if j.retiring {
		return
	}
	j.retiring, j.terminal = true, err
	s.observationChanged()
	for _, v := range j.needs {
		s.removeLease(v)
	}
	s.refresh(j)
	if j.cancel == nil {
		s.finish(j, err)
	}
}
func (s *state) finish(j *job, err error) {
	s.unqueue(j)
	s.removeDeadline(j)
	for _, v := range j.needs {
		s.removeLease(v)
	}
	delete(s.jobs, j.task.key)
	s.capacityAvailable()
	s.ungroup(j)
	if len(s.groups[j.need.Owner]) == 0 {
		delete(s.owners, j.need.Owner)
	}
	j.task.err = err
	close(j.task.done)
	notify(j.task.wake)
}
func (s *state) emit(kind, reason string, j *job) {
	s.observationChanged()
	if s.cfg.Observe == nil {
		return
	}
	e := Event{Kind: kind, Reason: reason, Active: s.active, Open: s.control.open, PrepareAhead: s.cfg.PrepareAhead}
	if j != nil {
		e.Key, e.Blocking, e.ReadyBy, e.WorkBytes = j.task.key, j.need.Blocking, j.need.ReadyBy, j.remaining
		e.Opportunistic = j.need.Opportunistic
		if kind == "started" && !j.need.ReadyBy.IsZero() {
			now := time.Now()
			e.Overdue = j.need.ReadyBy.Before(now)
		}
		if kind == "sample" {
			e.BlockTime = j.progress.ready.Sub(j.progress.admitted)
		}
	}
	s.cfg.Observe(e)
}
func (s *state) dispatch(now time.Time) {
	for {
		j := s.nextReady(now)
		if j == nil {
			return
		}
		// Blocking bypasses admission but remains part of active until cleanup.
		if !j.need.Blocking && s.active >= s.control.open {
			return
		}
		s.unqueue(j)
		s.sequence++
		s.owners[j.need.Owner] = s.sequence
		// Only this fairness group changes; deadlines in other groups do not.
		for other := range s.groups[j.need.Owner] {
			other.fair = s.sequence
			for _, h := range []*readyHeap{&s.ready, &s.untimed} {
				if index := other.index[h.slot]; index >= 0 {
					heap.Fix(h, index)
				}
			}
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		j.cancel, j.progress = cancel, new(Progress)
		j.progress.admitted, j.progress.work = time.Now(), j.remaining
		s.active++
		s.emit("started", "demand", j)
		go func() { s.s.finished <- completion{j, j.run(ctx, j.progress)} }()
	}
}

func (s *state) expireLeases(now time.Time) {
	for len(s.expiry) > 0 && !s.expiry[0].need.LeaseUntil.After(now) {
		v := heap.Pop(&s.expiry).(*lease)
		delete(v.job.needs, v.id)
		s.demandCount--
		s.emit("expired", "lease", v.job)
		s.refresh(v.job)
	}
}

func (s *state) loop() {
	defer close(s.s.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		now := time.Now()
		s.expireDeadlines(now)
		s.expireLeases(now)
		s.dispatch(now)
		s.regulate(now)
		if s.closing && s.active == 0 {
			return
		}
		timer.Stop()
		var tick <-chan time.Time
		next := s.nextControlEvent(now)
		if len(s.expiry) > 0 {
			next = earlier(next, s.expiry[0].need.LeaseUntil)
		}
		if !next.IsZero() {
			timer.Reset(max(0, time.Until(next)))
			tick = timer.C
		}
		select {
		case fn := <-s.s.commands:
			// A command may win selection against a due timer. Apply actual
			// expiry first so a new demand cannot revive the expired task.
			now := time.Now()
			s.expireDeadlines(now)
			s.expireLeases(now)
			fn(s)
		case c := <-s.s.finished:
			// Deadline wins when completion and its timer are both observable.
			// Readable bytes alone do not extend response/write cleanup time.
			s.expireDeadlines(time.Now())
			j := c.job
			if c.result.Err == nil && j.progress.comparable() {
				s.emit("sample", "readable_block", j)
			}
			j.cancel(nil)
			j.cancel = nil
			s.active--
			if j.retiring {
				err := j.terminal
				if c.result.Err != nil && !errors.Is(c.result.Err, context.Canceled) && !errors.Is(c.result.Err, context.DeadlineExceeded) {
					err = c.result.Err
				}
				s.finish(j, err)
			} else {
				s.emit("finished", "execution", j)
				s.finish(j, c.result.Err)
			}
		case <-tick:
		}
	}
}

type readyHeap struct {
	jobs []*job
	slot int
}

func (h readyHeap) Len() int    { return len(h.jobs) }
func (h readyHeap) first() *job { return h.jobs[0] }
func (h *readyHeap) set(j *job, member bool) {
	index := j.index[h.slot]
	if index >= 0 {
		if member {
			heap.Fix(h, index)
		} else {
			heap.Remove(h, index)
		}
	} else if member {
		heap.Push(h, j)
	}
}
func (h readyHeap) Less(i, j int) bool {
	a, b := h.jobs[i], h.jobs[j]
	if needBefore(a.need, b.need) {
		return true
	}
	if needBefore(b.need, a.need) {
		return false
	}
	if a.fair != b.fair {
		return a.fair < b.fair
	}
	return a.sequence < b.sequence
}
func (h readyHeap) Swap(i, j int) {
	h.jobs[i], h.jobs[j] = h.jobs[j], h.jobs[i]
	h.jobs[i].index[h.slot], h.jobs[j].index[h.slot] = i, j
}
func (h *readyHeap) Push(v any) {
	j := v.(*job)
	j.index[h.slot] = len(h.jobs)
	h.jobs = append(h.jobs, j)
}
func (h *readyHeap) Pop() any {
	a := h.jobs
	j := a[len(a)-1]
	a[len(a)-1] = nil
	h.jobs = a[:len(a)-1]
	j.index[h.slot] = -1
	return j
}

type leaseHeap []*lease

func (h leaseHeap) Len() int           { return len(h) }
func (h leaseHeap) Less(i, j int) bool { return h[i].need.LeaseUntil.Before(h[j].need.LeaseUntil) }
func (h leaseHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *leaseHeap) Push(v any)        { l := v.(*lease); l.index = len(*h); *h = append(*h, l) }
func (h *leaseHeap) Pop() any {
	a := *h
	l := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	l.index = -1
	return l
}
