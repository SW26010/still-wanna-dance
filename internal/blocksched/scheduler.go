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

type Progress struct{ bytes atomic.Int64 }

func (p *Progress) Add(n int64) {
	if n > 0 {
		p.bytes.Add(n)
	}
}

type Config struct {
	Limit, Background, MaxTasks, MaxDemands int
	// Optional event sink; it must be fast and must not call back into Scheduler.
	Observe func(Event)
}
type Event struct {
	Kind, Key, Reason    string
	Active, Open, Target int
	Blocking             bool
	ReadyBy              time.Time
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
		if v := s.lookup(d); v != nil {
			v.need = n
			heap.Fix(&s.expiry, v.index)
			s.refresh(v.job)
			ok = true
		}
	})
	return ok
}

// Close atomically withdraws the old contribution. A finite handoff is an
// independent lease, so a late Update cannot extend it or revive Blocking.
func (d *Demand) Close(handoff *Need) {
	d.task.s.call(func(s *state) {
		v := s.lookup(d)
		if v == nil {
			return
		}
		j := v.job
		s.removeLease(v)
		if handoff != nil && handoff.LeaseUntil.After(time.Now()) {
			s.sequence++
			n := *handoff
			n.Blocking = false
			v = &lease{id: s.sequence, job: j, need: n, index: -1}
			j.needs[v.id] = v
			s.demandCount++
			heap.Push(&s.expiry, v)
		}
		s.refresh(j)
	})
}

type job struct {
	task                  *Task
	run                   Run
	remaining             int64
	needs                 map[uint64]*lease
	need                  Need
	index                 int
	sequence, fair        uint64
	cancel                context.CancelCauseFunc
	progress              *Progress
	retiring, speculative bool
	terminal              error
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
	s                               *Scheduler
	cfg                             Config
	jobs                            map[string]*job
	ready                           readyHeap
	expiry                          leaseHeap
	owners                          map[string]uint64
	groups                          map[string]map[*job]bool
	sequence                        uint64
	active, background, demandCount int
	closing                         bool
}

func New(cfg Config) *Scheduler {
	cfg.Limit = max(1, cfg.Limit)
	cfg.Background = max(1, min(cfg.Limit, cfg.Background))
	if cfg.MaxTasks <= 0 {
		cfg.MaxTasks = 4096
	}
	if cfg.MaxDemands <= 0 {
		cfg.MaxDemands = 32768
	}
	s := &Scheduler{commands: make(chan func(*state), 64), finished: make(chan completion, cfg.Limit), done: make(chan struct{})}
	st := &state{s: s, cfg: cfg, jobs: make(map[string]*job), owners: make(map[string]uint64), groups: make(map[string]map[*job]bool)}
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
		st.jobs[key] = &job{task: t, run: run, remaining: remaining, needs: make(map[uint64]*lease), index: -1, sequence: st.sequence}
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
	return j.needs[d.id]
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
	s.ungroup(j)
	j.need = n
	if n.Owner != "" {
		if s.groups[n.Owner] == nil {
			s.groups[n.Owner] = make(map[*job]bool)
		}
		s.groups[n.Owner][j] = true
	}
	j.fair = s.owners[n.Owner]
	if n.Owner == "" || j.retiring {
		if j.index >= 0 {
			heap.Remove(&s.ready, j.index)
		}
		if j.cancel != nil {
			j.cancel(ErrPaused)
		}
		return
	}
	if j.cancel != nil {
		return
	}
	if j.index < 0 {
		heap.Push(&s.ready, j)
	} else {
		heap.Fix(&s.ready, j.index)
	}
}
func (s *state) ungroup(j *job) {
	owner := j.need.Owner
	delete(s.groups[owner], j)
	if len(s.groups[owner]) == 0 {
		delete(s.groups, owner)
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
	s.ungroup(j)
	if len(s.groups[j.need.Owner]) == 0 {
		delete(s.owners, j.need.Owner)
	}
	j.task.err = err
	close(j.task.done)
}
func (s *state) emit(kind, reason string, j *job) {
	if s.cfg.Observe != nil {
		s.cfg.Observe(Event{Kind: kind, Reason: reason, Key: j.task.key, Active: s.active, Open: s.cfg.Limit, Target: s.cfg.Limit, Blocking: j.need.Blocking, ReadyBy: j.need.ReadyBy})
	}
}
func (s *state) dispatch() {
	for s.ready.Len() > 0 && s.active < s.cfg.Limit {
		j := s.ready[0]
		if !j.need.Blocking && s.background >= s.cfg.Background {
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
		j.cancel, j.progress, j.speculative = cancel, new(Progress), !j.need.Blocking
		s.active++
		if j.speculative {
			s.background++
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
			s.refresh(v.job)
		}
		s.dispatch()
		if s.closing && s.active == 0 {
			return
		}
		timer.Stop()
		var tick <-chan time.Time
		if len(s.expiry) > 0 {
			timer.Reset(max(0, time.Until(s.expiry[0].need.LeaseUntil)))
			tick = timer.C
		}
		select {
		case fn := <-s.s.commands:
			fn(s)
		case c := <-s.s.finished:
			j := c.job
			j.cancel(nil)
			j.cancel = nil
			s.active--
			if j.speculative {
				s.background--
			}
			if j.retiring {
				s.finish(j, j.terminal)
			} else if errors.Is(c.result.Err, ErrPaused) {
				j.remaining = c.result.Remaining
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
