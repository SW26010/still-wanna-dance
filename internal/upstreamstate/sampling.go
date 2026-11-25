package upstreamstate

import (
	"context"
	"math/rand/v2"
	"sort"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// Internal probe budgets. Business request limits and recommendation thresholds
// are independent of how the monitor gathers its observations.
const (
	probeParallel       = 4
	probeAttempts       = 16
	probeSuccesses      = 3
	probeExploreTimeout = 7500 * time.Millisecond
)

type probeChoice struct {
	index    int
	priority float64
	timeout  time.Duration
}

func (m *Monitor) sampleRoutes(ctx context.Context, op Operation, route string, cs []upstreamrequest.Candidate, timeout time.Duration, probe func(context.Context, int) bool) {
	var plan []probeChoice
	target := len(cs)
	if len(cs) > probeParallel && m.experimentProbe == nil {
		plan = m.probePlan(op, route, cs, timeout)
		target = probeSuccesses
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout+min(timeout, probeExploreTimeout))
		defer cancel()
	} else {
		// Small pools fit in one wave. Offline experiments compare every path.
		for i := range cs {
			plan = append(plan, probeChoice{index: i, timeout: timeout})
		}
	}
	runProbeSample(ctx, plan, probeParallel, target, func(ctx context.Context, choice probeChoice) bool {
		if choice.timeout < timeout {
			// Ending exploration cancels this sample. Only a full attempt
			// or an earlier transport failure provides a timeout observation.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, choice.timeout)
			defer cancel()
		}
		return probe(ctx, choice.index)
	})
}

// Generate a weighted permutation without replacement. Each candidate has an
// exploration weight of one; fresh successes share an additional pool of equal
// total weight in proportion to their measured speed.
func (m *Monitor) probePlan(op Operation, route string, cs []upstreamrequest.Candidate, timeout time.Duration) []probeChoice {
	now := time.Now()
	plan := make([]probeChoice, len(cs))
	speeds := make([]float64, len(cs))
	var total float64
	leader := -1
	m.mu.Lock()
	for i, c := range cs {
		key := string(op) + "/" + route + "/" + c.ID
		r := m.resultKeyLocked(op, route, key, now)
		plan[i] = probeChoice{index: i, timeout: min(timeout, probeExploreTimeout)}
		if !recommendable(r) {
			continue
		}
		speeds[i] = score(r)
		total += speeds[i]
		// Give a previously successful path time to repeat its full read.
		repeat := time.Duration(2 * *r.ProbeDurationMS * float64(time.Millisecond))
		plan[i].timeout = max(plan[i].timeout, min(timeout, repeat))
		if pref := m.preferences[op]; pref != nil && pref.current == route+"/"+c.ID {
			leader = i
		}
	}
	m.mu.Unlock()
	for i, speed := range speeds {
		weight := 1.0
		if speed > 0 {
			weight += float64(len(cs)) * speed / total
		}
		plan[i].priority = rand.ExpFloat64() / weight
		if i == leader {
			plan[i].priority = -1
		}
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].priority < plan[j].priority })
	plan = plan[:min(len(plan), probeAttempts)]
	// Give two independent paths in the first wave a full read budget.
	// When a leader exists it takes the first slot, leaving one for a fallback.
	for i := range plan[:2] {
		plan[i].timeout = timeout
	}
	return plan
}

// Start probes in plan order. Stop admitting work once the target is met;
// already admitted probes may finish and supply independent evidence.
func runProbeSample[T any](ctx context.Context, plan []T, parallel, target int, probe func(context.Context, T) bool) {
	done := make(chan bool, parallel)
	running, successes := 0, 0
	for {
		for running < parallel && successes < target && len(plan) > 0 && ctx.Err() == nil {
			choice := plan[0]
			plan = plan[1:]
			running++
			go func() { done <- probe(ctx, choice) }()
		}
		if running == 0 {
			return
		}
		if <-done {
			successes++
		}
		running--
	}
}
