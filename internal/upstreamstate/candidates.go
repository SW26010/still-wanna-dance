package upstreamstate

import (
	"context"
	"sort"
	"sync"
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
}

func resultID(r Result) string { return r.Route + "/" + r.ChannelID }

func (m *Monitor) candidateResultsLocked(op Operation, provider upstreamrequest.Candidates, now time.Time) []Result {
	var out []Result
	for _, route := range routeIDs(op) {
		cs := provider.Current(entry(op, route))
		if len(cs) == 0 {
			r := Result{Operation: op, Route: route, Entry: entry(op, route), State: "unknown", Reason: "no_channel"}
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

func improves(best, current Result, threshold float64) bool {
	if best.Operation == Resource {
		return score(best) >= score(current)*(1+threshold)
	}
	return best.EstimatedLatencyMS != nil && current.EstimatedLatencyMS != nil && *best.EstimatedLatencyMS <= *current.EstimatedLatencyMS*(1-threshold)
}

// Recommended is a pure read. The executable channel is the one that was
// measured; no DNS lookup or fresh IP selection happens here.
func (m *Monitor) Recommended(op Operation) (Selection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	source := m.refreshChannelLocked()
	if m.closed || source.Candidates == nil {
		return Selection{}, false
	}
	results := m.candidateResultsLocked(op, source.Candidates, time.Now())
	var best *Result
	for i := range results {
		r := &results[i]
		if r.State != "available" {
			continue
		}
		if best == nil || score(*r) > score(*best) {
			best = r
		}
	}
	if pref := m.preferences[op]; pref != nil {
		for i := range results {
			if results[i].State == "available" && resultID(results[i]) == pref.current {
				best = &results[i]
				break
			}
		}
	}
	if best == nil {
		return Selection{}, false
	}
	for _, c := range source.Candidates.Current(best.Entry) {
		if c.ID == best.ChannelID {
			return Selection{Result: *best, Channel: c}, true
		}
	}
	return Selection{}, false
}

// Only completed checks count toward hysteresis; UI polling cannot cause switches.
func (m *Monitor) updatePreferencesLocked(provider upstreamrequest.Candidates, since time.Time) {
	if m.preferences == nil {
		m.preferences = make(map[Operation]*preference)
	}
	for _, op := range operations {
		var available []Result
		for _, r := range m.candidateResultsLocked(op, provider, time.Now()) {
			if r.State == "available" {
				available = append(available, r)
			}
		}
		sort.SliceStable(available, func(i, j int) bool { return score(available[i]) > score(available[j]) })
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
		if current == nil {
			pref.current, pref.challenger, pref.wins = resultID(best), "", 0
			continue
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

func (m *Monitor) checkCandidates(ctx context.Context, p Policy, source upstreamrequest.Snapshot, record func(observation)) {
	started := time.Now()
	provider := source.Candidates
	publish := func(o observation, c upstreamrequest.Candidate) {
		o.channel = c.ID
		m.mu.Lock()
		defer m.mu.Unlock()
		record(o)
	}
	// Limit concurrent transfers regardless of the number of DNS answers.
	parallel := func(cs []upstreamrequest.Candidate, f func(int, upstreamrequest.Candidate)) {
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i, c := range cs {
			if ctx.Err() != nil {
				break
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return
			}
			wg.Add(1)
			go func(i int, c upstreamrequest.Candidate) { defer wg.Done(); defer func() { <-sem }(); f(i, c) }(i, c)
		}
		wg.Wait()
	}
	prepare := func(target string) []upstreamrequest.Candidate {
		qctx, cancel := context.WithTimeout(ctx, p.RequestTimeout)
		defer cancel()
		cs, _ := provider.Candidates(qctx, target)
		return cs
	}
	// Populate resource hosts even without a playable sample, so snapshots
	// enumerate all their channels as unmeasured.
	for _, r := range videoRoutes {
		prepare(entry(Resource, r.id))
	}

	var ids []int64
	for _, route := range routeIDs(Catalog) {
		if ctx.Err() != nil {
			break
		}
		cs := prepare(entry(Catalog, route))
		samples := make([]int64, len(cs))
		parallel(cs, func(i int, c upstreamrequest.Candidate) {
			o, id := probeCatalogRoute(ctx, requestClient(c.Transport), p, route)
			samples[i] = id
			publish(o, c)
		})
		ids = append(ids, samples...)
	}

	m.mu.Lock()
	if m.refreshChannelLocked().Revision != source.Revision {
		m.mu.Unlock()
		return
	}
	for _, id := range ids {
		if id > 0 {
			m.songID, m.songAt = id, time.Now()
			break
		}
	}
	if !time.Now().Before(m.songAt.Add(p.SampleLifetime)) {
		m.songID = 0
	}
	id := m.songID
	m.mu.Unlock()
	if id > 0 {
		for _, r := range videoRoutes {
			cs := prepare(entry(PlaybackURL, r.id))
			samples := make([]*videoSample, len(cs))
			parallel(cs, func(i int, c upstreamrequest.Candidate) {
				o, s := probePlayback(ctx, requestClient(c.Transport), p, id, r)
				samples[i] = s
				publish(o, c)
			})
			var sample *videoSample
			for _, s := range samples {
				if s != nil {
					sample = s
					break
				}
			}
			if sample == nil {
				continue
			}
			resources := prepare(sample.url)
			// Throughput probes must not compete for the local connection.
			// Routes and their candidates share this serial resource phase.
			for _, c := range resources {
				if ctx.Err() != nil {
					break
				}
				publish(m.probeResourceWhenIdle(ctx, requestClient(c.Transport), p, id, r, *sample), c)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshChannelLocked().Revision == source.Revision {
		m.updatePreferencesLocked(provider, started)
		valid := make(map[string]bool)
		for _, op := range operations {
			for _, route := range routeIDs(op) {
				for _, c := range provider.Current(entry(op, route)) {
					valid[string(op)+"/"+route+"/"+c.ID] = true
				}
			}
		}
		for key := range m.history {
			if !valid[key] {
				delete(m.history, key)
			}
		}
	}
}
