package upstreamstate

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const throughputInterval = 15 * time.Minute

func (m *Monitor) scheduleThroughputLocked() {
	due := m.lastThroughput.Add(throughputInterval)
	if !m.lastThroughput.IsZero() && time.Now().Before(due) && due.Before(m.nextCheck) {
		m.nextCheck = due
	}
}

// Persist the start of an attempt, so failures and interrupted runs cannot
// cause large transfers on every scheduler wakeup or application restart.
func (m *Monitor) loadThroughputTime() error {
	if m.throughputStatePath == "" {
		return nil
	}
	b, err := os.ReadFile(m.throughputStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &m.lastThroughput)
}

func (m *Monitor) claimThroughput() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.batchThroughput != nil {
		return *m.batchThroughput
	}
	due := time.Since(m.lastThroughput) >= throughputInterval
	m.batchThroughput = &due
	if !due {
		return false
	}
	m.lastThroughput = time.Now()
	if m.throughputStatePath != "" {
		if err := m.saveThroughputTime(); err != nil {
			slog.Error("monitor_throughput_state_save_failed", "error", err)
			due = false
		}
	}
	return due
}

func (m *Monitor) saveThroughputTime() error {
	b, err := json.Marshal(m.lastThroughput)
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
