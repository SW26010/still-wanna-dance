package upstreamstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// Only sample metadata is persisted: no playable URLs or proxy credentials.
type ThroughputSample struct {
	ObservedAt time.Time     `json:"observedAt"`
	SongID     int64         `json:"songID"`
	Bytes      int64         `json:"bytes"`
	Duration   time.Duration `json:"duration"`
}

type throughputState struct {
	LastAttempt time.Time                   `json:"lastAttempt"`
	Samples     map[string]ThroughputSample `json:"samples"`
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
	// Upgrade the old timestamp-only file without resetting its cooldown.
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte(`"`)) {
		err = json.Unmarshal(b, &state.LastAttempt)
	} else {
		err = json.Unmarshal(b, &state)
	}
	if err != nil {
		slog.Warn("monitor_throughput_state_load_failed", "error", err)
		backup := m.throughputStatePath + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err := os.Rename(m.throughputStatePath, backup); err != nil {
			slog.Warn("monitor_throughput_state_quarantine_failed", "error", err)
		}
		return
	}
	m.lastThroughput = state.LastAttempt
	for key, sample := range state.Samples {
		if !restorableThroughputKey(key) {
			continue
		}
		if sample.Bytes <= 0 || sample.Duration <= 0 || sample.ObservedAt.IsZero() {
			continue
		}
		m.throughput[key] = observation{op: Resource, state: "available", at: sample.ObservedAt, songID: sample.SongID, bytes: sample.Bytes, transferDuration: sample.Duration}
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
	state := throughputState{LastAttempt: m.lastThroughput, Samples: make(map[string]ThroughputSample)}
	for key, o := range m.throughput {
		if !restorableThroughputKey(key) {
			continue
		}
		state.Samples[key] = ThroughputSample{ObservedAt: o.at, SongID: o.songID, Bytes: o.bytes, Duration: o.transferDuration}
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.throughputStatePath), ".throughput-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), m.throughputStatePath)
}

func restorableThroughputKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) < 2 || parts[0] != string(Resource) || (parts[1] != "cf" && parts[1] != "hkg") {
		return false
	}
	if len(parts) > 2 && parts[2] == "socks5" {
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
func (m *Monitor) pruneThroughputLocked(valid map[string]bool, prepared map[string]upstreamrequest.CandidateReadiness) {
	changed := false
	for key := range m.throughput {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 || valid[key] {
			continue
		}
		ready := prepared[entry(Resource, parts[1])]
		canPrune := ready.Direct
		if len(parts) == 3 && strings.HasPrefix(parts[2], "socks5/") {
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
