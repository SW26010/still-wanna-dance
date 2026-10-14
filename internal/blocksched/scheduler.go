// Package blocksched schedules opaque, resumable pieces of work. Callers own
// content identity and storage; the scheduler owns demand leases and execution.
package blocksched

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrPaused   = errors.New("block execution paused")
	ErrClosed   = errors.New("block scheduler closed")
	ErrCapacity = errors.New("block scheduler metadata capacity reached")
)

// Need separates urgency from validity. An overdue ReadyBy never cancels work.
// Owner is opaque and must be shared by duplicate/overlapping requests.
type Need struct {
	Owner               string
	ReadyBy, LeaseUntil time.Time
	Blocking            bool
}

type Result struct {
	Err       error
	Remaining int64
}

// Run must close bodies and save resumable progress before returning. A pause
// returns ErrPaused and the remaining cost. No buffer is needed while queued.
type Run func(context.Context, *Progress) Result

type Progress struct {
	bytes   atomic.Int64
	size    atomic.Int64
	invalid atomic.Bool
}

// SetSize corrects the execution cost after capability/response discovery.
func (p *Progress) SetSize(n int64) {
	if n > 0 {
		p.size.Store(n)
	}
}

// Invalidate excludes an execution with discarded/retried bytes from supply
// estimation. Transfer counters can still report its actual network traffic.
func (p *Progress) Invalidate() { p.invalid.Store(true) }

func (p *Progress) Add(n int64) {
	if n > 0 {
		p.bytes.Add(n)
	}
}

type Config struct {
	// SoftLimit bounds ordinary admission. Actual blocked reads bypass both
	// this limit and growth pacing; metadata capacity still bounds total work.
	SoftLimit, MaxTasks, MaxDemands      int
	GrowInterval, PreemptAfter, Cooldown time.Duration
	Adaptive                             bool
	// Optional event sink; it must be fast and must not call back into Scheduler.
	Observe func(Event)
}
type Event struct {
	Kind, Key, Reason    string
	Active, Open, Target int
	Blocking             bool
	ReadyBy              time.Time
	Remaining            int64
	Supply               float64
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
	t.s.call(func(s *state) {
		j := s.jobs[t.key]
		if j == nil || j.task != t || j.retiring || n.Owner == "" || s.demandCount >= s.cfg.MaxDemands || !n.LeaseUntil.After(time.Now()) {
			return
		}
		s.sequence++
		d.id = s.sequence
		v := &lease{id: d.id, job: j, need: n, index: -1}
		j.needs[d.id] = v
		s.demandCount++
		heap.Push(&s.expiry, v)
		s.refresh(j)
	})
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
	task             *Task
	run              Run
	remaining        int64
	initialRemaining int64
	needs            map[uint64]*lease
	need             Need
	index            int
	delayIndex       int
	sequence, fair   uint64
	cancel           context.CancelCauseFunc
	progress         *Progress
	retiring         bool
	yielding         bool
	terminal         error
	notBefore        time.Time
	started          time.Time
	sampled          int64
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
	running             map[*job]bool
	wanted, blocking    int
	ready               readyHeap
	expiry              leaseHeap
	owners              map[string]uint64
	groups              map[string]map[*job]bool
	sequence            uint64
	active, demandCount int
	closing             bool
	control             controller
	delayed             map[*job]bool
	sleeping            delayHeap
	contention          time.Time
}

func New(cfg Config) *Scheduler {
	cfg.SoftLimit = max(1, cfg.SoftLimit)
	if cfg.MaxTasks <= 0 {
		cfg.MaxTasks = 4096
	}
	if cfg.MaxDemands <= 0 {
		cfg.MaxDemands = 32768
	}
	s := &Scheduler{commands: make(chan func(*state), 64), finished: make(chan completion, cfg.SoftLimit), done: make(chan struct{})}
	st := &state{s: s, cfg: cfg, jobs: make(map[string]*job), running: make(map[*job]bool), owners: make(map[string]uint64), groups: make(map[string]map[*job]bool), delayed: make(map[*job]bool)}
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
func (s *Scheduler) Submit(key string, remaining int64, run Run) (*Task, error) {
	var t *Task
	var err error
	if !s.call(func(st *state) {
		if st.closing {
			err = ErrClosed
			return
		}
		if j := st.jobs[key]; j != nil {
			t = j.task
			return
		}
		if len(st.jobs) >= st.cfg.MaxTasks {
			err = ErrCapacity
			return
		}
		st.sequence++
		t = &Task{s: s, key: key, done: make(chan struct{})}
		st.jobs[key] = &job{task: t, run: run, remaining: remaining, needs: make(map[uint64]*lease), index: -1, delayIndex: -1, sequence: st.sequence}
	}) {
		return nil, ErrClosed
	}
	return t, err
}
func (s *Scheduler) Close() {
	s.once.Do(func() {
		s.call(func(st *state) {
			st.closing = true
			for _, j := range st.jobs {
				st.retire(j, ErrClosed)
			}
		})
	})
	<-s.done
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
}
func (s *state) refresh(j *job) {
	n := Need{}
	var earliest time.Time
	for _, v := range j.needs {
		if !v.need.LeaseUntil.After(time.Now()) {
			continue
		}
		if !v.need.ReadyBy.IsZero() && (earliest.IsZero() || v.need.ReadyBy.Before(earliest)) {
			earliest = v.need.ReadyBy
		}
		if n.Owner == "" || needBefore(v.need, n) || !needBefore(n, v.need) && v.need.Owner < n.Owner {
			n = v.need
		}
	}
	n.ReadyBy = earliest
	if j.need.Owner != n.Owner {
		s.ungroup(j)
	} else if n.Owner != "" {
		s.wanted--
		if j.need.Blocking {
			s.blocking--
		}
	}
	j.need = n
	if n.Owner != "" {
		s.wanted++
		if n.Blocking {
			s.blocking++
		}
		if s.groups[n.Owner] == nil {
			s.groups[n.Owner] = make(map[*job]bool)
		}
		s.groups[n.Owner][j] = true
	}
	j.fair = s.owners[n.Owner]
	if n.Owner == "" || j.retiring {
		s.removeDelayed(j)
		if j.index >= 0 {
			heap.Remove(&s.ready, j.index)
		}
		if j.cancel != nil {
			j.cancel(ErrPaused)
			j.yielding = true
		}
		return
	}
	if j.cancel != nil {
		return
	}
	if !n.Blocking && (!j.notBefore.IsZero() && time.Now().Before(j.notBefore) || s.delayed[j] && s.hasBlocking()) {
		s.addDelayed(j)
		if j.index >= 0 {
			heap.Remove(&s.ready, j.index)
		}
		return
	}
	s.removeDelayed(j)
	if j.index < 0 {
		heap.Push(&s.ready, j)
		s.emit("queued", "demand", j)
	} else {
		heap.Fix(&s.ready, j.index)
	}
}
func (s *state) ungroup(j *job) {
	owner := j.need.Owner
	if !s.groups[owner][j] {
		return
	}
	s.wanted--
	if j.need.Blocking {
		s.blocking--
	}
	delete(s.groups[owner], j)
	if len(s.groups[owner]) == 0 {
		delete(s.groups, owner)
		delete(s.owners, owner)
	}
}
func needBefore(a, b Need) bool {
	if a.Blocking != b.Blocking {
		return a.Blocking
	}
	if a.Blocking {
		return false
	}
	if a.ReadyBy.IsZero() != b.ReadyBy.IsZero() {
		return !a.ReadyBy.IsZero()
	}
	return a.ReadyBy.Before(b.ReadyBy)
}
func (s *state) retire(j *job, err error) {
	j.retiring, j.terminal = true, err
	for _, v := range j.needs {
		s.removeLease(v)
	}
	s.refresh(j)
	if j.cancel == nil {
		s.finish(j, err)
	}
}
func (s *state) finish(j *job, err error) {
	if j.index >= 0 {
		heap.Remove(&s.ready, j.index)
	}
	for _, v := range j.needs {
		s.removeLease(v)
	}
	delete(s.jobs, j.task.key)
	s.removeDelayed(j)
	s.ungroup(j)
	if len(s.groups[j.need.Owner]) == 0 {
		delete(s.owners, j.need.Owner)
	}
	j.task.err = err
	close(j.task.done)
	notify(j.task.wake)
}
func (s *state) emit(kind, reason string, j *job) {
	if s.cfg.Observe == nil {
		return
	}
	e := Event{Kind: kind, Reason: reason, Active: s.active, Open: s.control.open, Target: s.control.target, Supply: s.control.rate}
	if j != nil {
		e.Key, e.Blocking, e.ReadyBy, e.Remaining = j.task.key, j.need.Blocking, j.need.ReadyBy, j.remaining
	}
	s.cfg.Observe(e)
}
func (s *state) dispatch() {
	for s.ready.Len() > 0 {
		j := s.ready[0]
		if !j.need.Blocking && s.active >= s.control.open {
			return
		}
		heap.Pop(&s.ready)
		s.sequence++
		s.owners[j.need.Owner] = s.sequence
		// Only this fairness group changes; deadlines in other groups do not.
		for other := range s.groups[j.need.Owner] {
			other.fair = s.sequence
			if other.index >= 0 {
				heap.Fix(&s.ready, other.index)
			}
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		j.cancel, j.progress = cancel, new(Progress)
		j.started, j.sampled, j.yielding = time.Now(), 0, false
		s.active++
		s.running[j] = true
		j.initialRemaining = j.remaining
		if s.cfg.GrowInterval > 0 && s.control.nextSample.IsZero() {
			s.control.nextSample = time.Now().Add(sampleInterval)
		}
		s.emit("started", "demand", j)
		go func() { s.s.finished <- completion{j, j.run(ctx, j.progress)} }()
	}
}
func (s *state) loop() {
	defer close(s.s.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		now := time.Now()
		for len(s.expiry) > 0 && !s.expiry[0].need.LeaseUntil.After(now) {
			v := heap.Pop(&s.expiry).(*lease)
			delete(v.job.needs, v.id)
			s.demandCount--
			s.emit("expired", "lease", v.job)
			s.refresh(v.job)
		}
		if !s.hasBlocking() {
			for len(s.sleeping) > 0 && !s.sleeping[0].notBefore.After(now) {
				s.refresh(s.sleeping[0])
			}
		}
		s.regulate(now)
		s.dispatch()
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
			fn(s)
		case c := <-s.s.finished:
			j := c.job
			s.sampleProgress(j, time.Now())
			j.cancel(nil)
			j.cancel = nil
			s.active--
			delete(s.running, j)
			if j.retiring {
				err := j.terminal
				if c.result.Err != nil && !errors.Is(c.result.Err, ErrPaused) && !errors.Is(c.result.Err, context.Canceled) && !errors.Is(c.result.Err, context.DeadlineExceeded) {
					err = c.result.Err
				}
				s.finish(j, err)
			} else if errors.Is(c.result.Err, ErrPaused) {
				j.remaining = c.result.Remaining
				j.notBefore = time.Now().Add(s.cfg.Cooldown)
				s.addDelayed(j)
				s.emit("paused", "lease_or_contention", j)
				s.refresh(j)
			} else {
				s.emit("finished", "execution", j)
				s.finish(j, c.result.Err)
			}
		case <-tick:
		}
	}
}

type readyHeap []*job

func (h readyHeap) Len() int { return len(h) }
func (h readyHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
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
func (h readyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *readyHeap) Push(v any)   { j := v.(*job); j.index = len(*h); *h = append(*h, j) }
func (h *readyHeap) Pop() any {
	a := *h
	j := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	j.index = -1
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

func (s *state) addDelayed(j *job) {
	s.delayed[j] = true
	if j.delayIndex < 0 {
		heap.Push(&s.sleeping, j)
	} else {
		heap.Fix(&s.sleeping, j.delayIndex)
	}
}
func (s *state) removeDelayed(j *job) {
	delete(s.delayed, j)
	if j.delayIndex >= 0 {
		heap.Remove(&s.sleeping, j.delayIndex)
	}
}

type delayHeap []*job

func (h delayHeap) Len() int           { return len(h) }
func (h delayHeap) Less(i, j int) bool { return h[i].notBefore.Before(h[j].notBefore) }
func (h delayHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].delayIndex = i; h[j].delayIndex = j }
func (h *delayHeap) Push(v any)        { j := v.(*job); j.delayIndex = len(*h); *h = append(*h, j) }
func (h *delayHeap) Pop() any {
	a := *h
	j := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	j.delayIndex = -1
	return j
}
