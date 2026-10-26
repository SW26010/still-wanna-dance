package blocksched

import (
	"sort"
	"sync"
	"time"
)

// ReadRequests connects local missing reads to the same task keys used by the
// resource executor. All mutable fields belong to the scheduler goroutine.
// Close ends this execution session; an old reader cannot affect its successor.
type ReadRequests struct {
	s          *Scheduler
	owner      string
	key        func(int64) string
	wake       chan<- struct{}
	waits      map[*readWait]bool
	boundBlock *int64 // One executor may satisfy several initially independent reads.
	closed     bool
}

type readWait struct {
	requests *ReadRequests
	block    int64
	at       time.Time
}

// key must be a pure mapping to the opaque keys passed to Schedule.
func (s *Scheduler) NewReadRequests(owner string, key func(int64) string, wake chan<- struct{}) *ReadRequests {
	return &ReadRequests{s: s, owner: owner, key: key, wake: wake, waits: make(map[*readWait]bool)}
}

func (r *ReadRequests) Key(block int64) string { return r.key(block) }

// Wait records one actual missing read, even before a task exists. The local
// reader must end it on return, error or cancellation. Repeated cleanup is safe.
func (r *ReadRequests) Wait(block int64) func() {
	w := &readWait{requests: r, block: block, at: time.Now()}
	r.s.call(func(s *state) {
		if r.closed || s.closing {
			return
		}
		if r.boundBlock != nil {
			w.block = *r.boundBlock
		}
		key := r.Key(w.block)
		r.waits[w] = true
		if s.reads[key] == nil {
			s.reads[key] = make(map[*readWait]bool)
		}
		s.reads[key][w] = true
		s.observationChanged()
		if j := s.jobs[key]; j != nil && !j.retiring {
			s.refresh(j)
		}
		notify(r.wake)
	})
	return sync.OnceFunc(func() { r.s.call(func(s *state) { s.endRead(w) }) })
}

// Bind associates all reads with one execution block once the resource loader
// discovers that a single response supplies them. Move existing waits atomically,
// preserving their original timestamps and independent cancellation handles.
func (r *ReadRequests) Bind(block int64) {
	r.s.call(func(s *state) {
		if r.closed || s.closing || r.boundBlock != nil {
			return
		}
		r.boundBlock = &block
		key := r.Key(block)
		changed := map[string]bool{key: true}
		for w := range r.waits {
			old := r.Key(w.block)
			delete(s.reads[old], w)
			if len(s.reads[old]) == 0 {
				delete(s.reads, old)
			}
			changed[old] = true
			w.block = block
			if s.reads[key] == nil {
				s.reads[key] = make(map[*readWait]bool)
			}
			s.reads[key][w] = true
		}
		for key := range changed {
			if j := s.jobs[key]; j != nil && !j.retiring {
				s.refresh(j)
			}
		}
		s.observationChanged()
		notify(r.wake)
	})
}

// Requested exposes only block identities needing an executor, never urgency,
// timestamps or reader identities. Ordinary usage is supplied independently.
func (r *ReadRequests) Requested() map[int64]bool {
	blocks := make(map[int64]bool)
	r.s.call(func(s *state) {
		for w := range r.waits {
			blocks[w.block] = true
		}
	})
	return blocks
}

// OrderRegistration puts requested data before ordinary candidates so a small
// task capacity cannot fill with new executions before a waiting read registers.
// The coordinator follows this order without receiving any urgency attributes.
func (r *ReadRequests) OrderRegistration(blocks []int64) {
	requested := r.Requested()
	sort.Slice(blocks, func(i, j int) bool {
		if a, b := requested[blocks[i]], requested[blocks[j]]; a != b {
			return a
		}
		return blocks[i] < blocks[j]
	})
}

func (r *ReadRequests) Close() {
	r.s.call(func(s *state) {
		r.closed = true
		for w := range r.waits {
			s.endRead(w)
		}
	})
}

func (s *state) endRead(w *readWait) {
	r := w.requests
	if !r.waits[w] {
		return
	}
	key := r.Key(w.block)
	delete(r.waits, w)
	delete(s.reads[key], w)
	s.observationChanged()
	if len(s.reads[key]) == 0 {
		delete(s.reads, key)
	}
	if j := s.jobs[key]; j != nil && !j.retiring {
		s.refresh(j)
	}
	notify(r.wake)
}

func (s *state) withReads(key string, n Need) priority {
	p := priority{Need: n}
	for w := range s.reads[key] {
		if p.Owner == "" {
			p.Owner = w.requests.owner
		}
		p.Blocking = true
		p.ReadyBy = earlier(p.ReadyBy, w.at)
	}
	return p
}
