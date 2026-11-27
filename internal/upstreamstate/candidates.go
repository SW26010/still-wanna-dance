package upstreamstate

import (
	"net/url"
	"sort"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type Selection struct {
	Result  Result
	Channel upstreamrequest.Candidate
}
type preference struct {
	current, challenger string
	wins                int
	throughputVote      time.Time
}

func resultID(r Result) string { return r.Route + "/" + r.ChannelID }

func (m *Monitor) candidateResultsLocked(op Operation, view *upstreamrequest.CandidateView, now time.Time) []Result {
	var out []Result
	for _, route := range m.routeIDsLocked(op) {
		cs := view.Current(entry(op, route))
		sources := m.sourcesLocked(op, route)
		if len(cs) == 0 {
			r := Result{Operation: op, Route: route, Entry: entry(op, route), State: "unknown", Reason: "no_channel", Sources: sources}
			if m.closed {
				r.State, r.Reason = "closed", ""
			}
			out = append(out, r)
			continue
		}
		for _, c := range cs {
			r := m.resultKeyLocked(op, route, string(op)+"/"+route+"/"+c.ID, now)
			r.Sources = sources
			r.ChannelID, r.Mode, r.IP = c.ID, c.Mode, c.IP
			if !c.ValidUntil.IsZero() && (r.ValidUntil.IsZero() || c.ValidUntil.Before(r.ValidUntil)) {
				r.ValidUntil = c.ValidUntil
			}
			out = append(out, r)
		}
	}
	return out
}
func recommendable(r Result) bool {
	if r.State != "available" {
		return false
	}
	if r.Operation == Resource {
		return r.EstimatedSpeedBPS != nil || r.EstimatedLatencyMS != nil
	}
	return responseTime(r) != nil
}

func responseTime(r Result) *float64 {
	if r.Operation == Catalog {
		return r.ProbeDurationMS
	}
	return r.EstimatedLatencyMS
}

func score(r Result) float64 {
	if r.Operation == Resource {
		if r.EstimatedSpeedBPS != nil {
			return *r.EstimatedSpeedBPS
		}
		return 0
	}
	if ms := responseTime(r); ms != nil {
		return 1 / max(*ms, 0.001)
	}
	return 0
}

func preferred(a, b Result) bool {
	return score(a) > score(b)
}

func improves(best, current Result, threshold float64) bool {
	if best.Operation == Resource {
		return score(best) >= score(current)*(1+threshold)
	}
	bestMS, currentMS := responseTime(best), responseTime(current)
	return bestMS != nil && currentMS != nil && *bestMS <= *currentMS*(1-threshold)
}

// Recommended is a pure read. The executable channel is the one that was
// measured; no DNS lookup or fresh IP selection happens here.
func (m *Monitor) Recommended(op Operation) (Selection, bool) {
	selections := m.Selections(op, Constraints{})
	if len(selections) == 0 {
		return Selection{}, false
	}
	return selections[0], true
}

// Constraints restrict recommendations before ranking. Entry matches an exact
// operation endpoint; Target restricts resource channels to its origin host.
type Constraints struct {
	Route, Entry, Target, Mode string
}

func (q Constraints) matches(r Result) bool {
	if q.Route != "" && q.Route != r.Route || q.Entry != "" && q.Entry != r.Entry || q.Mode != "" && q.Mode != r.Mode {
		return false
	}
	if q.Target != "" {
		u, err := url.Parse(q.Target)
		e, entryErr := url.Parse(r.Entry)
		if err != nil || entryErr != nil || u.Hostname() == "" || u.Hostname() != e.Hostname() {
			return false
		}
	}
	return true
}

// Selections is a bounded, I/O-free snapshot of measured executable channels.
// Preserve the hysteresis winner when it satisfies the caller's constraints.
func (m *Monitor) Selections(op Operation, q Constraints) []Selection {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.selectionsLocked(m.refreshChannelLocked(), op, q)
}

func (m *Monitor) selectionsLocked(source upstreamrequest.Snapshot, op Operation, q Constraints) []Selection {
	if m.closed || source.Candidates == nil {
		return nil
	}
	now := time.Now()
	view := m.candidateViewLocked(source.Candidates, now)
	results := m.candidateResultsLocked(op, view, now)
	return m.selectResultsLocked(op, q, results, view, len(results))
}

func (m *Monitor) selectResultsLocked(op Operation, q Constraints, results []Result, view *upstreamrequest.CandidateView, limit int) []Selection {
	var eligible []Result
	for i := range results {
		r := &results[i]
		if r.Mode != "direct" || !recommendable(*r) || !q.matches(*r) {
			continue
		}
		eligible = append(eligible, *r)
	}
	sort.SliceStable(eligible, func(i, j int) bool { return preferred(eligible[i], eligible[j]) })
	if pref := m.preferences[op]; pref != nil {
		for i := range eligible {
			if resultID(eligible[i]) == pref.current &&
				(op != Resource || eligible[i].EstimatedSpeedBPS != nil || eligible[0].EstimatedSpeedBPS == nil) {
				winner := eligible[i]
				copy(eligible[1:i+1], eligible[:i])
				eligible[0] = winner
				break
			}
		}
	}
	var selections []Selection
	for _, r := range eligible {
		if c, ok := view.Find(r.Entry, r.ChannelID); ok && c.Transport != nil {
			selections = append(selections, Selection{Result: r, Channel: c})
			if len(selections) == limit {
				return selections
			}
		}
	}
	return selections
}

// Only completed checks count toward hysteresis; UI polling cannot cause switches.
func (m *Monitor) updatePreferencesLocked(provider upstreamrequest.Candidates, since time.Time, kind CheckKind) {
	now := time.Now()
	view := m.candidateViewLocked(provider, now)
	if m.preferences == nil {
		m.preferences = make(map[Operation]*preference)
	}
	for _, op := range operations {
		if !kind.includes(op) {
			continue
		}
		var available []Result
		for _, r := range m.candidateResultsLocked(op, view, now) {
			if r.Mode == "direct" && recommendable(r) {
				available = append(available, r)
			}
		}
		sort.SliceStable(available, func(i, j int) bool { return preferred(available[i], available[j]) })
		if len(available) == 0 {
			delete(m.preferences, op)
			continue
		}
		best := available[0]
		pref := m.preferences[op]
		if pref == nil {
			m.preferences[op] = &preference{current: resultID(best)}
			continue
		}
		var current *Result
		for i := range available {
			if resultID(available[i]) == pref.current {
				current = &available[i]
			}
		}
		if current == nil ||
			op == Resource && current.EstimatedSpeedBPS == nil && best.EstimatedSpeedBPS != nil {
			pref.current, pref.challenger, pref.wins = resultID(best), "", 0
			continue
		}
		if op == Resource {
			// Lightweight checks refresh availability, not throughput evidence.
			// Preserve pending wins between actual throughput measurements.
			if best.ThroughputObservedAt.IsZero() || best.ThroughputObservedAt.Before(since) || !best.ThroughputObservedAt.After(pref.throughputVote) {
				continue
			}
			pref.throughputVote = best.ThroughputObservedAt
		}
		if resultID(best) == pref.current || best.ObservedAt.Before(since) || !improves(best, *current, m.policy.SwitchImprovement) {
			pref.challenger, pref.wins = "", 0
			continue
		}
		if pref.challenger != resultID(best) {
			pref.challenger, pref.wins = resultID(best), 0
		}
		pref.wins++
		if pref.wins >= m.policy.SwitchSamples {
			pref.current, pref.challenger, pref.wins = resultID(best), "", 0
		}
	}
}

func (m *Monitor) scheduleCandidatesLocked(provider upstreamrequest.Candidates) {
	now := time.Now()
	view := m.candidateViewLocked(provider, now)
	// Refresh measurements before their own expiry. DNS leases only govern
	// candidate eligibility; the resolver refreshes them and signals changes.
	for _, op := range []Operation{Catalog, PlaybackURL} {
		results := m.candidateResultsLocked(op, view, now)
		// A sampled pool can intentionally leave healthy alternatives untouched.
		// Scheduling each of their expirations would repeatedly wake the monitor.
		if len(results) > probeParallel && m.experimentProbe == nil {
			winner := m.selectResultsLocked(op, Constraints{}, results, view, 1)
			if len(winner) == 0 {
				continue
			}
			results = []Result{winner[0].Result}
		}
		for _, r := range results {
			if r.State != "available" {
				continue
			}
			margin := min(m.policy.Lifetime/3, m.policy.RequestTimeout*2+m.policy.ResourceTimeout)
			next := r.ObservedAt.Add(m.policy.Lifetime - margin)
			if floor := time.Now().Add(time.Second); next.Before(floor) {
				next = floor
			}
			if next.Before(m.nextCheck) {
				m.nextCheck = next
			}
		}
	}
}
