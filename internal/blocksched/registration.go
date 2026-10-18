package blocksched

import "time"

// CapacityChanged is a broadcast snapshot. Take it before trying registration
// so completion, withdrawal and priority changes cannot cause a lost wakeup.
func (s *Scheduler) CapacityChanged() <-chan struct{} {
	changed := s.done
	s.call(func(st *state) { changed = st.capacityChanged })
	return changed
}

func (s *state) capacityAvailable() {
	close(s.capacityChanged)
	s.capacityChanged = make(chan struct{})
}

// Schedule registers a task and its first demand atomically. At capacity only
// an idle, lower priority task may be displaced; its owner gets ErrDeferred and
// retains responsibility for registering it again. Executions are never evicted.
func (s *Scheduler) Schedule(key string, remaining int64, run Run, n Need) (*Task, *Demand, error) {
	var task *Task
	var demand *Demand
	var err error
	if !s.call(func(st *state) {
		if st.closing {
			err = ErrClosed
			return
		}
		now := time.Now()
		if n.Owner == "" || !n.LeaseUntil.After(now) {
			err = ErrCapacity
			return
		}
		if Expired(n.ReadyBy, now) {
			err = ErrExpired
			return
		}
		j := st.jobs[key]
		if j != nil && j.retiring {
			err = ErrCapacity
			return
		}
		if j == nil && len(st.jobs) >= st.cfg.MaxTasks || st.demandCount >= st.cfg.MaxDemands {
			var victim *job
			for _, candidate := range st.jobs {
				if st.demandCount >= st.cfg.MaxDemands && len(candidate.needs) == 0 {
					continue
				}
				if candidate == j || candidate.cancel != nil || candidate.retiring || !st.registrationBefore(n, candidate.need, now) {
					continue
				}
				if victim == nil || st.registrationBefore(victim.need, candidate.need, now) {
					victim = candidate
				}
			}
			if victim == nil {
				err = ErrCapacity
				return
			}
			st.retire(victim, ErrDeferred)
		}
		if j == nil {
			st.sequence++
			task = &Task{s: s, key: key, done: make(chan struct{})}
			j = &job{task: task, run: run, remaining: remaining, needs: make(map[uint64]*lease), index: [2]int{-1, -1}, deadlineIndex: -1, sequence: st.sequence}
			st.jobs[key] = j
		}
		task = j.task
		// Reuse the ordinary lease path while still in the scheduler command.
		demand = st.add(task, n)
	}) {
		return nil, nil, ErrClosed
	}
	return task, demand, err
}

func (s *state) registrationBefore(a, b Need, now time.Time) bool {
	if b.Owner == "" {
		return a.Owner != ""
	}
	if a.Blocking != b.Blocking {
		return a.Blocking
	}
	eligible := func(n Need) bool {
		return n.Blocking || n.Opportunistic || n.ReadyBy.IsZero() || !n.ReadyBy.Add(-s.cfg.PrepareAhead).After(now)
	}
	if eligible(a) != eligible(b) {
		return eligible(a)
	}
	return needBefore(a, b)
}
