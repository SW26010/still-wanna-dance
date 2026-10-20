package blocksched

import (
	"sync"
	"time"
)

// Observation is a read-only view. It never holds task handles or extends work.
type Observation struct {
	Tasks        map[string]TaskObservation
	Active, Open int
}

type TaskObservation struct {
	Running, Retiring bool
	Readers           int
	Deadline          time.Time
}

// WatchChanges installs a nonblocking, coalescing notification sink. The caller
// owns wake and must not close it. Without subscribers there is no observer
// allocation, timer, goroutine, event history or snapshot work in the scheduler.
func (s *Scheduler) WatchChanges(wake chan<- struct{}) func() {
	s.call(func(st *state) {
		if st.observers == nil {
			st.observers = make(map[chan<- struct{}]bool)
		}
		st.observers[wake] = true
	})
	return sync.OnceFunc(func() {
		s.call(func(st *state) {
			delete(st.observers, wake)
			if len(st.observers) == 0 {
				st.observers = nil
			}
		})
	})
}

func (s *state) observationChanged() {
	for wake := range s.observers {
		notify(wake)
	}
}

func (s *Scheduler) Observation() Observation {
	result := Observation{Tasks: make(map[string]TaskObservation)}
	s.call(func(st *state) {
		result.Active, result.Open = st.active, st.control.open
		for key, j := range st.jobs {
			result.Tasks[key] = TaskObservation{Running: j.cancel != nil, Retiring: j.retiring, Readers: len(st.reads[key]), Deadline: Deadline(j.need.ReadyBy)}
		}
		for key, readers := range st.reads {
			if _, ok := result.Tasks[key]; !ok {
				result.Tasks[key] = TaskObservation{Readers: len(readers)}
			}
		}
	})
	return result
}
