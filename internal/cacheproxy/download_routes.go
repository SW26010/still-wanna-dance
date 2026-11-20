package cacheproxy

import (
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/videometa"
	"still-wanna-dance/internal/workfiles"
)

const (
	routeLifetime    = 24 * time.Hour
	routeRefresh     = 10 * time.Minute
	sampleInterval   = 5 * time.Minute
	alternateIPLimit = 30 * time.Second
)

type routeKey struct{ host, ip string }

// A successful duration describes one entire validated 1 MiB network attempt.
type RouteObservation struct {
	Host          string        `json:"host"`
	IP            string        `json:"ip,omitempty"`
	ObservedAt    time.Time     `json:"observedAt"`
	Source        string        `json:"source"`
	Failed        bool          `json:"failed,omitempty"`
	BlockDuration time.Duration `json:"blockDurationNs,omitempty"`
	BlockAt       time.Time     `json:"blockAt,omitzero"`
}

type routeEntry struct {
	RouteObservation
	successAt     time.Time
	successSource string
	inFlight      int
	lastSample    time.Time
	lease         time.Time
	repeated      bool
	touched       time.Time
}

// DownloadRoutes belongs to one active upstream configuration. DNS eligibility
// is supplied at selection time and never restored from observations.
type DownloadRoutes struct {
	mu               sync.Mutex
	direct           bool
	proxySampleAfter time.Time
	entries          map[routeKey]*routeEntry
	path             string
	timer            *time.Timer
	saveMu           sync.Mutex
	dirty            bool
}

func NewDownloadRoutes(direct bool, path string) *DownloadRoutes {
	h := &DownloadRoutes{direct: direct, entries: make(map[routeKey]*routeEntry)}
	if direct && path != "" {
		h.path = path
		h.restore()
	}
	return h
}

func (h *DownloadRoutes) entry(k routeKey, now time.Time) *routeEntry {
	e := h.entries[k]
	if e == nil {
		if len(h.entries) >= 1024 {
			var victim routeKey
			var oldest time.Time
			for key, v := range h.entries {
				if v.inFlight == 0 && (oldest.IsZero() || v.touched.Before(oldest)) {
					victim, oldest = key, v.touched
				}
			}
			if !oldest.IsZero() {
				delete(h.entries, victim)
			}
		}
		e = &routeEntry{RouteObservation: RouteObservation{Host: k.host, IP: k.ip}}
		h.entries[k] = e
	}
	e.touched = now
	return e
}

func (e *routeEntry) cooling(now time.Time) bool {
	return e.Failed && now.Sub(e.ObservedAt) < time.Minute
}
func (e *routeEntry) measured(now time.Time) bool {
	return e.BlockDuration > 0 && now.Sub(e.BlockAt) < routeLifetime
}

func (e *routeEntry) sampleDue(now time.Time, skipInterval bool) bool {
	return e.inFlight == 0 && (skipInterval || (now.Sub(e.lastSample) >= sampleInterval && now.Sub(e.ObservedAt) >= sampleInterval))
}

func preferRoute(a, b *routeEntry, now time.Time, urgent bool) bool {
	if a.cooling(now) != b.cooling(now) {
		return !a.cooling(now)
	}
	if !urgent {
		staleA := a.inFlight == 0 && now.Sub(a.ObservedAt) >= routeRefresh
		staleB := b.inFlight == 0 && now.Sub(b.ObservedAt) >= routeRefresh
		if staleA != staleB {
			return staleA
		}
		if staleA && !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
	}
	if a.measured(now) != b.measured(now) {
		return a.measured(now)
	}
	if urgent && a.measured(now) && a.BlockDuration != b.BlockDuration {
		return a.BlockDuration < b.BlockDuration
	}
	if urgent && !a.measured(now) && a.repeated != b.repeated {
		return a.repeated
	}
	if a.inFlight != b.inFlight {
		return a.inFlight < b.inFlight
	}
	if a.measured(now) && a.BlockDuration != b.BlockDuration {
		return a.BlockDuration < b.BlockDuration
	}
	return a.repeated && !b.repeated
}

// Choose and reserve under one lock so parallel workers see each other's load.
func (h *DownloadRoutes) choose(host string, candidates []upstreamrequest.Candidate, tried map[string]bool, needBy time.Time, sample *blockSample) (upstreamrequest.Candidate, int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	remaining := needBy.Sub(now)
	urgent := !needBy.IsZero() && remaining <= alternateIPLimit
	type measuredCandidate struct {
		index int
		entry *routeEntry
	}
	var measured []measuredCandidate
	var best *routeEntry
	var selected upstreamrequest.Candidate
	count := 0
	hasValid := false
	for i, c := range candidates {
		if c.Host != host || c.Mode != "direct" || c.Transport == nil || (!c.ValidUntil.IsZero() && !now.Before(c.ValidUntil)) {
			continue
		}
		if _, err := netip.ParseAddr(c.IP); err != nil {
			continue
		}
		hasValid = true
		if tried[c.IP] || (sample != nil && sample.target.ip != "" && (host != sample.target.host || c.IP != sample.target.ip)) {
			continue
		}
		key := routeKey{host, c.IP}
		e := h.entry(key, now)
		if c.ValidUntil.After(e.lease) {
			e.repeated = !e.lease.IsZero()
			e.lease = c.ValidUntil
		}
		if sample != nil && !e.sampleDue(now, sample.manual || sample.visited[key]) {
			continue
		}
		if urgent && e.measured(now) {
			measured = append(measured, measuredCandidate{i, e})
		}
		if best == nil || preferRoute(e, best, now, urgent) {
			best, selected = e, c
		}
		count++
	}
	if len(measured) > 1 && best.measured(now) && best.BlockDuration <= remaining {
		// Anchor the 10% window to the fastest eligible route for this attempt.
		// Pairwise comparisons could drift to progressively slower idle routes.
		fastest := best.BlockDuration
		for _, candidate := range measured {
			e := candidate.entry
			if e.cooling(now) != best.cooling(now) || e.BlockDuration > remaining || e.BlockDuration-fastest > fastest/10 {
				continue
			}
			if e.inFlight < best.inFlight || (e.inFlight == best.inFlight && preferRoute(e, best, now, true)) {
				best, selected = e, candidates[candidate.index]
			}
		}
	}
	if best != nil {
		key := routeKey{host, selected.IP}
		best = h.entry(key, now)
		best.inFlight++
		if sample != nil {
			best.lastSample = now
			sample.visited[key] = true
		}
	}
	return selected, count, hasValid
}

func (h *DownloadRoutes) release(k routeKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries[k].inFlight--
}

func (h *DownloadRoutes) record(k routeKey, source string, bytes int64, failed bool, elapsed time.Duration) {
	if (bytes <= 0 && !failed) || (h.direct && k.ip == "") {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if !h.direct {
		h.proxySampleAfter = now.Add(sampleInterval)
	}
	e := h.entry(k, now)
	e.ObservedAt, e.Source, e.Failed = now, source, failed
	if bytes > 0 {
		e.successAt, e.successSource = now, source
		if bytes == DefaultBlockSize {
			e.BlockDuration, e.BlockAt = elapsed, now
		}
		if h.path != "" {
			h.dirty = true
			if h.timer == nil {
				h.timer = time.AfterFunc(5*time.Second, h.Flush)
			}
		}
	}
}

func (h *DownloadRoutes) Snapshot() []RouteObservation {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	now := time.Now()
	var rows []RouteObservation
	for _, e := range h.entries {
		if now.Sub(e.ObservedAt) >= routeLifetime {
			continue
		}
		row := e.RouteObservation
		if !e.measured(now) {
			row.BlockAt = time.Time{}
			row.BlockDuration = 0
		}
		rows = append(rows, row)
	}
	h.mu.Unlock()
	slices.SortFunc(rows, func(a, b RouteObservation) int {
		if n := cmp.Compare(a.Host, b.Host); n != 0 {
			return n
		}
		aCooling, bCooling := a.Failed && now.Sub(a.ObservedAt) < time.Minute, b.Failed && now.Sub(b.ObservedAt) < time.Minute
		if aCooling != bCooling {
			if aCooling {
				return 1
			}
			return -1
		}
		if (a.BlockDuration == 0) != (b.BlockDuration == 0) {
			if a.BlockDuration == 0 {
				return 1
			}
			return -1
		}
		if n := cmp.Compare(a.BlockDuration, b.BlockDuration); n != 0 {
			return n
		}
		return cmp.Compare(a.IP, b.IP)
	})
	return rows
}

type routeFile struct {
	Version   int                `json:"version"`
	Successes []RouteObservation `json:"successes"`
}

func (h *DownloadRoutes) restore() {
	b, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var state routeFile
	if err == nil {
		err = json.Unmarshal(b, &state)
	}
	if err != nil {
		slog.Warn("下载线路历史读取失败", "error", err)
		return
	}
	if state.Version != 1 {
		return
	}
	now := time.Now()
	for _, r := range state.Successes {
		if _, err := netip.ParseAddr(r.IP); err != nil || !videometa.ValidHost(r.Host) || r.Failed || r.ObservedAt.After(now) || now.Sub(r.ObservedAt) >= routeLifetime || (r.Source != "download" && r.Source != "sample") {
			continue
		}
		if r.BlockDuration <= 0 || r.BlockAt.After(r.ObservedAt) || now.Sub(r.BlockAt) >= routeLifetime {
			r.BlockDuration = 0
			r.BlockAt = time.Time{}
		}
		e := h.entry(routeKey{r.Host, r.IP}, now)
		e.RouteObservation, e.successAt, e.successSource = r, r.ObservedAt, r.Source
	}
}

// Flush coalesces busy downloads and serializes atomic writes outside the selection lock.
func (h *DownloadRoutes) Flush() {
	if h == nil {
		return
	}
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	h.mu.Lock()
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	if !h.dirty {
		h.mu.Unlock()
		return
	}
	h.dirty = false
	state := routeFile{Version: 1}
	for _, e := range h.entries {
		if time.Since(e.successAt) >= routeLifetime {
			continue
		}
		r := e.RouteObservation
		r.ObservedAt, r.Source, r.Failed = e.successAt, e.successSource, false
		state.Successes = append(state.Successes, r)
	}
	h.mu.Unlock()
	b, err := json.Marshal(state)
	if err == nil {
		err = workfiles.Write(h.path, b)
	}
	if err != nil {
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
		slog.Warn("下载线路历史保存失败", "error", err)
	}
}
