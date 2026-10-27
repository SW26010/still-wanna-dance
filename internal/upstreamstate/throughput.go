package upstreamstate

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/videometa"
	"still-wanna-dance/internal/workfiles"
)

// Only sample metadata is persisted: no playable URLs or proxy credentials.
type ThroughputSample struct {
	ObservedAt time.Time     `json:"observedAt"`
	SongID     int64         `json:"songID"`
	Bytes      int64         `json:"bytes"`
	Duration   time.Duration `json:"durationNs"`
}

const throughputSchemaVersion = 2

type throughputState struct {
	SchemaVersion int                         `json:"schemaVersion"`
	LastAttempt   time.Time                   `json:"lastAttempt"`
	Samples       map[string]ThroughputSample `json:"samples"`
}

func (m *Monitor) scheduleThroughputLocked() {
	due := m.lastThroughput.Add(m.policy.ThroughputInterval)
	if !m.lastThroughput.IsZero() && time.Now().Before(due) && due.Before(m.nextCheck) {
		m.nextCheck = due
	}
}

// Restore the cooldown and historical samples only after decoding succeeds.
// Unavailable observation caches must never prevent monitor startup.
func (m *Monitor) loadThroughputTime() {
	if m.throughputStatePath == "" {
		return
	}
	b, err := os.ReadFile(m.throughputStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("monitor_throughput_state_load_failed", "error", err)
		return
	}
	var state throughputState
	err = json.Unmarshal(b, &state)
	if err != nil {
		slog.Warn("monitor_throughput_state_load_failed", "error", err)
		// These observations are rebuildable. Leave the original until the
		// next successful atomic save; do not accumulate diagnostic copies.
		return
	}
	if state.SchemaVersion != throughputSchemaVersion {
		return // Rebuild incompatible observations; never interpret legacy formats.
	}
	m.lastThroughput = state.LastAttempt.UTC()
	if m.lastThroughput.After(time.Now()) {
		m.lastThroughput = time.Time{}
	}
	for key, sample := range state.Samples {
		if !restorableThroughputKey(key) {
			continue
		}
		if sample.Bytes <= 0 || sample.Duration <= 0 || sample.ObservedAt.IsZero() || sample.ObservedAt.After(time.Now()) {
			continue
		}
		if existing, ok := m.throughput[key]; !ok || sample.ObservedAt.After(existing.at) {
			m.throughput[key] = observation{op: Resource, state: "available", at: sample.ObservedAt, songID: sample.SongID, bytes: sample.Bytes, transferDuration: sample.Duration}
		}
	}
}

// Persist the start of an attempt, so failures and interrupted runs cannot
// cause large transfers on every scheduler wakeup or application restart.
func (m *Monitor) claimThroughput(manual bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.batchThroughput != nil {
		return *m.batchThroughput
	}
	due := manual || time.Since(m.lastThroughput) >= m.policy.ThroughputInterval
	m.batchThroughput = &due
	if !due {
		return false
	}
	// Keep the monotonic reading for live cooldowns; persistence converts to UTC.
	m.lastThroughput = time.Now()
	if m.throughputStatePath != "" {
		if err := m.saveThroughputTime(); err != nil {
			slog.Error("monitor_throughput_state_save_failed", "error", err)
			// An explicit check still runs if persistence is unavailable;
			// the in-memory cooldown has already been reset.
			due = manual
			m.throughputSaveFailed = true
		} else {
			m.throughputSaveFailed = false
		}
	}
	return due
}

func (m *Monitor) saveThroughputTime() error {
	state := throughputState{SchemaVersion: throughputSchemaVersion, LastAttempt: m.lastThroughput.UTC(), Samples: make(map[string]ThroughputSample)}
	for key, o := range m.throughput {
		if !restorableThroughputKey(key) {
			continue
		}
		state.Samples[key] = ThroughputSample{ObservedAt: o.at.UTC(), SongID: o.songID, Bytes: o.bytes, Duration: o.transferDuration}
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return workfiles.Write(m.throughputStatePath, b)
}

func restorableThroughputKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) < 3 || parts[0] != string(Resource) || !videometa.ValidHost(parts[1]) || parts[2] == "" {
		return false
	}
	if parts[2] == "socks5" {
		return len(parts) == 5 && upstreamrequest.PersistentProxyID(parts[4])
	}
	return true
}

func (m *Monitor) persistThroughputResult() {
	if m.throughputStatePath == "" {
		return
	}
	err := m.saveThroughputTime()
	m.throughputSaveFailed = err != nil
	if err != nil {
		slog.Error("monitor_throughput_result_save_failed", "error", err)
	}
}

// Called with m.mu held after candidate discovery, independently of history.
func (m *Monitor) pruneThroughputLocked(valid map[string]bool, prepared map[string]upstreamrequest.CandidateReadiness, provider upstreamrequest.Candidates) {
	var known map[string]bool
	authoritative := true
	if membership, ok := provider.(upstreamrequest.ProxyMembership); ok {
		var ids []string
		ids, authoritative = membership.KnownProxyIDs()
		known = make(map[string]bool, len(ids))
		for _, id := range ids {
			known[id] = true
		}
	}
	changed := false
	for key := range m.throughput {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 || valid[key] {
			continue
		}
		ready := prepared[entry(Resource, parts[1])]
		canPrune := ready.Direct
		if len(parts) == 3 && strings.HasPrefix(parts[2], "socks5/") {
			identity := strings.Split(parts[2], "/")
			if !authoritative || len(identity) == 3 && known[identity[2]] {
				continue
			}
			canPrune = ready.Proxy
		}
		if !canPrune {
			continue
		}
		delete(m.throughput, key)
		changed = true
	}
	if changed {
		m.persistThroughputResult()
	}
}

func (m *Monitor) throughputStatus(s Status, now time.Time) string {
	if s.Closed {
		return "closed"
	}
	if s.ResourcesPaused {
		return "business_busy"
	}
	if s.Checking && m.batchThroughput != nil && *m.batchThroughput {
		return "running"
	}
	if s.Manual && s.Checking {
		if m.batchKind == CheckThroughput || m.batchKind == "" {
			return "preparing"
		}
		return "other_manual_check"
	}
	if !s.Scheduled {
		return "not_scheduled"
	}
	for _, r := range s.Results {
		if r.Operation == Resource && r.LocalDiagnostic != "" && !r.LocalObservedAt.Before(m.lastThroughput) {
			return r.LocalDiagnostic
		}
	}
	if now.Before(s.NextThroughput) {
		return "cooldown"
	}
	if m.throughputSaveFailed {
		return "persistence_error"
	}
	if s.Checking {
		return "preparing"
	}
	for _, r := range s.Results {
		if r.Operation == PlaybackURL && r.State == "available" {
			return "due"
		}
	}
	return "waiting_sample"
}
