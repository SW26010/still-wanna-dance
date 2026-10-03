// Package upstreamstate checks fixed upstream services and exposes the best
// observed entry for each operation. Requests follow the application's channel.
package upstreamstate

import "time"

type Operation string

const (
	Catalog     Operation = "catalog"
	PlaybackURL Operation = "playback_url"
	Resource    Operation = "resource"
)

var operations = [...]Operation{Catalog, PlaybackURL, Resource}

// Zero durations use defaults; negative durations are invalid. SetPolicy
// replaces the whole policy, including timeouts for subsequent checks.
type Policy struct {
	Interval        time.Duration `json:"interval"`
	Lifetime        time.Duration `json:"lifetime"`
	FailureLifetime time.Duration `json:"failureLifetime"`
	SampleLifetime  time.Duration `json:"sampleLifetime"`
	RequestTimeout  time.Duration `json:"requestTimeout"`
	ResourceTimeout time.Duration `json:"resourceTimeout"`
}

func DefaultPolicy() Policy {
	return Policy{
		Interval:        5 * time.Minute,
		Lifetime:        6 * time.Minute,
		FailureLifetime: 30 * time.Second,
		SampleLifetime:  10 * time.Minute,
		RequestTimeout:  30 * time.Second,
		ResourceTimeout: 4 * time.Second,
	}
}
func normalized(p Policy) Policy {
	d := DefaultPolicy()
	if p.Interval == 0 {
		p.Interval = d.Interval
	}
	if p.Lifetime == 0 {
		p.Lifetime = d.Lifetime
	}
	if p.FailureLifetime == 0 {
		p.FailureLifetime = d.FailureLifetime
	}
	if p.SampleLifetime == 0 {
		p.SampleLifetime = d.SampleLifetime
	}
	if p.RequestTimeout == 0 {
		p.RequestTimeout = d.RequestTimeout
	}
	if p.ResourceTimeout == 0 {
		p.ResourceTimeout = d.ResourceTimeout
	}
	return p
}

// Nil estimates mean unavailable/not applicable, not zero. Entry is a service
// entry, never a resource URL that can be substituted for an arbitrary video.
type Result struct {
	Operation          Operation `json:"operation"`
	State              string    `json:"state"` // available, unavailable, unknown, stale, closed
	Reason             string    `json:"reason,omitempty"`
	Stage              string    `json:"stage,omitempty"`
	HTTP               int       `json:"http,omitempty"`
	Entry              string    `json:"entry,omitempty"`
	Route              string    `json:"route,omitempty"`
	EstimatedLatencyMS *float64  `json:"estimatedLatencyMS"`
	EstimatedSpeedBPS  *float64  `json:"estimatedSpeedBPS"`
	ProbeDurationMS    *float64  `json:"probeDurationMS"`
	ObservedAt         time.Time `json:"observedAt"`
	ValidUntil         time.Time `json:"validUntil"`
	Samples            int       `json:"samples"`
	// SampleSongID is the requested API song, not the resource ID in its URL.
	SampleSongID int64  `json:"sampleSongID,omitempty"`
	Basis        string `json:"basis,omitempty"`
}
type observation struct {
	op                  Operation
	route, state, stage string
	http                int
	at                  time.Time
	latency, duration   time.Duration
	bytes               int64
	songID              int64
}
type route struct{ id, node, host string }

var videoRoutes = [...]route{{"cf", "cf", "play.udon.dance"}, {"hkg", "nya", "nya.xin.moe"}}

const apiBase = "https://api.udon.dance"

func allowedVideoHost(host string) bool {
	for _, r := range videoRoutes {
		if r.host == host {
			return true
		}
	}
	return false
}

func candidates(op Operation) []string {
	if op == Catalog {
		return []string{"api"}
	}
	if op == PlaybackURL || op == Resource {
		ids := make([]string, len(videoRoutes))
		for i, r := range videoRoutes {
			ids[i] = r.id
		}
		return ids
	}
	return nil
}
func entry(op Operation, id string) string {
	if op == Catalog {
		return apiBase + "/Api/Songs/list"
	}
	for _, r := range videoRoutes {
		if r.id == id {
			if op == PlaybackURL {
				return apiBase + "/Api/Songs/play?node=" + r.node
			}
			if op == Resource {
				return "https://" + r.host
			}
		}
	}
	return ""
}
func validUntil(o observation, p Policy) time.Time {
	ttl := p.FailureLifetime
	if o.state == "available" {
		ttl = p.Lifetime
	}
	return o.at.Add(ttl)
}

// All following helpers are called with m.mu held.
func (m *Monitor) record(o observation) {
	if m.closed || o.state == "canceled" {
		return
	}
	key := string(o.op) + "/" + o.route
	h := m.history[key]
	if len(h) > 0 {
		last := h[len(h)-1]
		if o.at.Before(last.at) {
			return
		}
		if last.state != "available" || o.state != "available" || last.songID != o.songID {
			h = nil
		}
	}
	h = append(h, o)
	if len(h) > 8 {
		h = h[len(h)-8:]
	}
	m.history[key] = h
}
func (m *Monitor) candidate(op Operation, id string, now time.Time) Result {
	r := Result{Operation: op, State: "unknown", Reason: "no_sample"}
	h := m.history[string(op)+"/"+id]
	if len(h) == 0 {
		return r
	}
	last := h[len(h)-1]
	r.ObservedAt = last.at
	r.ValidUntil = validUntil(last, m.policy)
	r.SampleSongID = last.songID
	r.HTTP = last.http
	r.Stage = last.stage
	if !now.Before(r.ValidUntil) {
		r.State = "stale"
		r.Reason = "expired"
		return r
	}
	if last.state != "available" {
		r.State = "unavailable"
		r.Reason = last.state
		return r
	}
	r.State = "available"
	r.Reason = ""
	r.Entry = entry(op, id)
	r.Route = id
	var latency, duration, speed float64
	var speedSamples int
	for _, o := range h {
		if o.state == "available" && now.Before(validUntil(o, m.policy)) {
			r.Samples++
			latency += float64(o.latency) / float64(time.Millisecond)
			duration += float64(o.duration) / float64(time.Millisecond)
			if o.bytes > 0 && o.duration > 0 {
				speed += float64(o.bytes) / o.duration.Seconds()
				speedSamples++
			}
		}
	}
	latency /= float64(r.Samples)
	duration /= float64(r.Samples)
	r.EstimatedLatencyMS = &latency
	r.ProbeDurationMS = &duration
	if speedSamples > 0 && op != PlaybackURL {
		speed /= float64(speedSamples)
		r.EstimatedSpeedBPS = &speed
	}
	r.Basis = "completion_latency"
	if op == Resource && r.EstimatedSpeedBPS != nil {
		r.Basis = "bounded_transfer"
	}
	return r
}

// commitPreferencesLocked publishes hysteresis anchors once per completed batch.
// Partial observations remain readable without giving the first finisher priority.
func (m *Monitor) commitPreferencesLocked(now time.Time) {
	for _, op := range operations {
		r := m.bestLocked(op, now)
		if r.State == "available" {
			m.preferred[op] = r.Route
		} else {
			delete(m.preferred, op)
		}
	}
}

// bestLocked computes a result without changing the committed preference.
func (m *Monitor) bestLocked(op Operation, now time.Time) Result {
	result := Result{Operation: op, State: "unknown", Reason: "no_sample"}
	if m.closed {
		result.State = "closed"
		result.Reason = ""
		return result
	}
	ids := candidates(op)
	if len(ids) == 0 {
		result.Reason = "unsupported_operation"
		return result
	}
	var available, failures, stale []Result
	for _, id := range ids {
		r := m.candidate(op, id, now)
		switch r.State {
		case "available":
			available = append(available, r)
		case "unavailable":
			failures = append(failures, r)
		case "stale":
			stale = append(stale, r)
		}
	}
	if len(available) == 0 {
		if len(failures) == len(ids) {
			result = failures[0]
			if len(ids) > 1 {
				result.Reason = "all_entries_failed"
				result.HTTP = 0
				result.Stage = ""
			}
		}
		if len(stale) > 0 && len(stale)+len(failures) == len(ids) {
			result = stale[0]
		}
		return result
	}
	best := available[0]
	better := func(a, b Result) bool {
		if op == Resource && a.EstimatedSpeedBPS != nil && b.EstimatedSpeedBPS != nil {
			return *a.EstimatedSpeedBPS > *b.EstimatedSpeedBPS
		}
		return *a.ProbeDurationMS < *b.ProbeDurationMS
	}
	for _, r := range available[1:] {
		if better(r, best) {
			best = r
		}
	}
	for _, old := range available {
		if old.Route == m.preferred[op] && old.Basis == best.Basis {
			// Hysteresis applies only to comparable, fresh, successful samples.
			if op == Resource && old.EstimatedSpeedBPS != nil && best.EstimatedSpeedBPS != nil {
				if *best.EstimatedSpeedBPS < *old.EstimatedSpeedBPS*1.2 {
					best = old
				}
			} else if *best.ProbeDurationMS > *old.ProbeDurationMS*.8 {
				best = old
			}
		}
	}
	return best
}
