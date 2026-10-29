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

func (m *Monitor) candidateResultsLocked(op Operation, provider upstreamrequest.Candidates, now time.Time) []Result {
	var out []Result
	for _, route := range m.routeIDsLocked(op) {
		var cs []upstreamrequest.Candidate
		if provider != nil {
			cs = provider.Current(entry(op, route))
		}
		if len(cs) == 0 {
			r := Result{Operation: op, Route: route, Entry: entry(op, route), State: "unknown", Reason: "no_channel", Sources: m.sourcesLocked(op, route, now)}
			if m.closed {
				r.State, r.Reason = "closed", ""
			}
			out = append(out, r)
			continue
		}
		for _, c := range cs {
			r := m.resultKeyLocked(op, route, string(op)+"/"+route+"/"+c.ID, now)
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
		return r.EstimatedSpeedBPS != nil || observedScheme(r.Scheme) == "https" && r.EstimatedLatencyMS != nil
	}
	return r.EstimatedLatencyMS != nil
}

func score(r Result) float64 {
	if r.Operation == Resource {
		if r.EstimatedSpeedBPS != nil {
			return *r.EstimatedSpeedBPS
		}
		return 0
	}
	if r.EstimatedLatencyMS == nil {
		return 0
	}
	return 1 / max(*r.EstimatedLatencyMS, 0.001)
}

func preferred(a, b Result) bool {
	if observedScheme(a.Scheme) != observedScheme(b.Scheme) {
		return observedScheme(a.Scheme) == "https"
	}
	return score(a) > score(b)
}

func improves(best, current Result, threshold float64) bool {
	if best.Operation == Resource {
		return score(best) >= score(current)*(1+threshold)
	}
	return best.EstimatedLatencyMS != nil && current.EstimatedLatencyMS != nil && *best.EstimatedLatencyMS <= *current.EstimatedLatencyMS*(1-threshold)
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
	source := m.refreshChannelLocked()
	if m.closed || source.Candidates == nil {
		return nil
	}
	results := m.candidateResultsLocked(op, source.Candidates, time.Now())
	var eligible []Result
	for i := range results {
		r := &results[i]
		if !recommendable(*r) || !q.matches(*r) {
			continue
		}
		eligible = append(eligible, *r)
	}
	sort.SliceStable(eligible, func(i, j int) bool { return preferred(eligible[i], eligible[j]) })
	if pref := m.preferences[op]; pref != nil {
		for i := range eligible {
			if resultID(eligible[i]) == pref.current && observedScheme(eligible[i].Scheme) == observedScheme(eligible[0].Scheme) &&
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
		for _, c := range source.Candidates.Current(r.Entry) {
			if c.ID == r.ChannelID && c.Transport != nil {
				selections = append(selections, Selection{Result: r, Channel: c})
				break
			}
		}
	}
	return selections
}

// Only completed checks count toward hysteresis; UI polling cannot cause switches.
func (m *Monitor) updatePreferencesLocked(provider upstreamrequest.Candidates, since time.Time, kind CheckKind) {
	if m.preferences == nil {
		m.preferences = make(map[Operation]*preference)
	}
	for _, op := range operations {
		if !kind.includes(op) {
			continue
		}
		var available []Result
		for _, r := range m.candidateResultsLocked(op, provider, time.Now()) {
			if recommendable(r) {
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
		if current == nil || observedScheme(best.Scheme) == "https" && observedScheme(current.Scheme) == "http" ||
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
	// Refresh before measurement expiry; DNS supplies its own change signal.
	// A one-second floor prevents tiny TTLs/failures creating a busy loop.
	for _, op := range operations {
		for _, r := range m.candidateResultsLocked(op, provider, time.Now()) {
			if r.State != "available" {
				continue
			}
			margin := min(m.policy.Lifetime/3, m.policy.RequestTimeout*2+m.policy.ResourceTimeout)
			next := r.ValidUntil.Add(-margin)
			if floor := time.Now().Add(time.Second); next.Before(floor) {
				next = floor
			}
			if next.Before(m.nextCheck) {
				m.nextCheck = next
			}
		}
	}
}
