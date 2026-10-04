// Package upstreamstate checks fixed upstream services and exposes independent
// observations for every operation and route. Requests follow the application's channel.
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
	Interval          time.Duration `json:"interval"`
	Lifetime          time.Duration `json:"lifetime"`
	FailureLifetime   time.Duration `json:"failureLifetime"`
	SampleLifetime    time.Duration `json:"sampleLifetime"`
	RequestTimeout    time.Duration `json:"requestTimeout"`
	ResourceMaxBytes  int64         `json:"resourceMaxBytes"`
	ResourceTimeout   time.Duration `json:"resourceTimeout"`
	SwitchImprovement float64       `json:"switchImprovement"`
	SwitchSamples     int           `json:"switchSamples"`
}

func DefaultPolicy() Policy {
	return Policy{
		Interval:          5 * time.Minute,
		Lifetime:          6 * time.Minute,
		FailureLifetime:   30 * time.Second,
		SampleLifetime:    10 * time.Minute,
		RequestTimeout:    30 * time.Second,
		ResourceMaxBytes:  16 << 20,
		ResourceTimeout:   3 * time.Second,
		SwitchImprovement: 0.15,
		SwitchSamples:     2,
	}
}
func normalized(p Policy) Policy {
	d := DefaultPolicy()
	if p.SwitchImprovement == 0 {
		p.SwitchImprovement = d.SwitchImprovement
	}
	if p.SwitchSamples == 0 {
		p.SwitchSamples = d.SwitchSamples
	}
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
	if p.ResourceMaxBytes == 0 {
		p.ResourceMaxBytes = d.ResourceMaxBytes
	}
	if p.ResourceTimeout == 0 {
		p.ResourceTimeout = d.ResourceTimeout
	}
	return p
}

// Nil estimates mean unavailable/not applicable, not zero. Route and Entry
// identify the checked service in every state; they are not recommendations.
// A resource Entry is never a playable URL for an arbitrary video.
type Result struct {
	ThroughputSongID     int64     `json:"throughputSongID,omitempty"`
	ThroughputObservedAt time.Time `json:"throughputObservedAt"`
	// CatalogTime preserves the upstream response time verbatim, not the probe time.
	CatalogTime        string    `json:"catalogTime,omitempty"`
	ChannelID          string    `json:"channelID,omitempty"`
	Mode               string    `json:"mode,omitempty"`
	IP                 string    `json:"ip,omitempty"`
	Operation          Operation `json:"operation"`
	State              string    `json:"state"` // available, unavailable, unknown, stale, closed
	Reason             string    `json:"reason,omitempty"`
	Stage              string    `json:"stage,omitempty"`
	HTTP               int       `json:"http,omitempty"`
	Entry              string    `json:"entry"`
	Route              string    `json:"route"`
	EstimatedLatencyMS *float64  `json:"estimatedLatencyMS"`
	EstimatedSpeedBPS  *float64  `json:"estimatedSpeedBPS"`
	TransferDurationMS *float64  `json:"transferDurationMS"`
	TransferredBytes   int64     `json:"transferredBytes"`
	ProbeDurationMS    *float64  `json:"probeDurationMS"`
	ObservedAt         time.Time `json:"observedAt"`
	ValidUntil         time.Time `json:"validUntil"`
	Samples            int       `json:"samples"`
	// SampleSongID is the requested API song, not the resource ID in its URL.
	SampleSongID int64 `json:"sampleSongID,omitempty"`
}
type observation struct {
	catalogTime         string
	channel             string
	op                  Operation
	route, state, stage string
	http                int
	at                  time.Time
	latency, duration   time.Duration
	transferDuration    time.Duration
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

func routeIDs(op Operation) []string {
	if op == Catalog {
		return []string{"api", "kiva", "wanna"}
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
		if id == "wanna" {
			return "https://wanna.kiva.moe/api/wannaInfo"
		}
		if id == "kiva" {
			return "https://x.kiva.moe/api/v2/wanna/songs"
		}
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
	if o.channel != "" {
		key += "/" + o.channel
	}
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
	if o.op == Resource && o.state == "available" && o.bytes > 0 {
		if o.transferDuration <= 0 {
			o.transferDuration = o.duration
		}
		if o.transferDuration <= 0 {
			return
		}
		m.throughput[key] = o
	}
}
func (m *Monitor) resultLocked(op Operation, id string, now time.Time) Result {
	return m.resultKeyLocked(op, id, string(op)+"/"+id, now)
}
func (m *Monitor) resultKeyLocked(op Operation, id, key string, now time.Time) (r Result) {
	r = Result{Operation: op, Route: id, Entry: entry(op, id), State: "unknown", Reason: "no_sample"}
	defer func() {
		if op != Resource || r.State != "available" {
			return
		}
		r.EstimatedSpeedBPS, r.TransferDurationMS, r.TransferredBytes = nil, nil, 0
		if o, ok := m.throughput[key]; ok && now.Before(o.at.Add(throughputInterval)) {
			speed, ms := float64(o.bytes)/o.transferDuration.Seconds(), float64(o.transferDuration)/float64(time.Millisecond)
			r.EstimatedSpeedBPS, r.TransferDurationMS, r.TransferredBytes = &speed, &ms, o.bytes
			r.ThroughputObservedAt = o.at
			r.ThroughputSongID = o.songID
		}
	}()
	if m.closed {
		r.State, r.Reason = "closed", ""
		return r
	}
	h := m.history[key]
	if len(h) == 0 {
		return r
	}
	last := h[len(h)-1]
	r.CatalogTime = last.catalogTime
	r.ObservedAt = last.at
	r.ValidUntil = validUntil(last, m.policy)
	r.SampleSongID = last.songID
	r.HTTP = last.http
	r.Stage = last.stage
	r.TransferredBytes = last.bytes
	if last.transferDuration > 0 {
		ms := float64(last.transferDuration) / float64(time.Millisecond)
		r.TransferDurationMS = &ms
	}
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
	var latency, duration, speed float64
	var speedSamples int
	for _, o := range h {
		if o.state == "available" && now.Before(validUntil(o, m.policy)) {
			r.Samples++
			latency += float64(o.latency) / float64(time.Millisecond)
			duration += float64(o.duration) / float64(time.Millisecond)
			measuredDuration := o.duration
			if o.op == Resource && o.transferDuration > 0 {
				measuredDuration = o.transferDuration
			}
			if o.bytes > 0 && measuredDuration > 0 {
				speed += float64(o.bytes) / measuredDuration.Seconds()
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
	return r
}

// resultsLocked preserves route definition order, regardless of measurements.
func (m *Monitor) resultsLocked(op Operation, now time.Time) []Result {
	if source := m.channel.Snapshot(); source.Candidates != nil {
		return m.candidateResultsLocked(op, source.Candidates, now)
	}
	ids := routeIDs(op)
	results := make([]Result, 0, len(ids))
	for _, id := range ids {
		results = append(results, m.resultLocked(op, id, now))
	}
	return results
}
