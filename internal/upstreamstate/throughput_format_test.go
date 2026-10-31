package upstreamstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestThroughputVersionAndClockRollback(t *testing.T) {
	old, err := os.ReadFile("testdata/throughput-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{
		string(old),
		`{"lastAttempt":"2026-01-01T00:00:00Z"}`,
		`{"schemaVersion":999,"lastAttempt":"2026-01-01T00:00:00Z"}`,
		`{"schemaVersion":2,"lastAttempt":"2099-01-01T00:00:00Z"}`,
	} {
		path := filepath.Join(t.TempDir(), "throughput.json")
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		m, err := newMonitor(Options{ThroughputStatePath: path}, upstreamrequest.NewChannel())
		if err != nil {
			t.Fatal(err)
		}
		if !m.lastThroughput.IsZero() || len(m.throughput) != 0 {
			t.Fatal("restored incompatible/future state")
		}
		if unchanged, err := os.ReadFile(path); err != nil || string(unchanged) != document {
			t.Fatal("loading rewrote unsupported observations", err)
		}
		m.Close()
	}
}

func TestThroughputUTCAndDurationContract(t *testing.T) {
	m := testMonitor(t, fixture)
	m.throughputStatePath = filepath.Join(t.TempDir(), "throughput.json")
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 8*3600))
	m.lastThroughput = at
	m.throughput["resource/play.udon.dance/direct/play.udon.dance/192.0.2.1"] = observation{at: at, bytes: 42, transferDuration: 123456789, songID: 1}
	if err := m.saveThroughputTime(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(m.throughputStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "+08:00") || !strings.Contains(string(b), `"durationNs":123456789`) || !strings.Contains(string(b), "123456789Z") {
		t.Fatal(string(b))
	}
	var state throughputState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != throughputSchemaVersion || !state.LastAttempt.Equal(at) {
		t.Fatal(state)
	}
}

func TestThroughputClaimPreservesMonotonicCooldown(t *testing.T) {
	m := testMonitor(t, fixture)
	m.throughputStatePath = filepath.Join(t.TempDir(), "throughput.json")
	if !m.claimThroughput(false) {
		t.Fatal("initial throughput attempt was not claimed")
	}
	// Round(0) preserves the wall time and location, removing only the
	// monotonic reading. Equality here would make cooldowns clock-sensitive.
	claimed := m.lastThroughput
	if claimed == claimed.Round(0) {
		t.Fatal("claim discarded the monotonic clock")
	}
	if err := m.saveThroughputTime(); err != nil {
		t.Fatal(err)
	}
	if m.lastThroughput != claimed {
		t.Fatal("persistence changed the in-memory cooldown clock")
	}
	b, err := os.ReadFile(m.throughputStatePath)
	if err != nil {
		t.Fatal(err)
	}
	var state throughputState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if !state.LastAttempt.Equal(claimed) || state.LastAttempt.Location() != time.UTC {
		t.Fatal("persisted attempt must represent the same instant in UTC")
	}
	m.batchThroughput = nil
	if m.claimThroughput(false) {
		t.Fatal("immediate automatic retry bypassed cooldown")
	}
}

func TestLegacyThroughputV2RetainsCooldownOnly(t *testing.T) {
	b, err := os.ReadFile("testdata/throughput-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "throughput.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := newMonitor(Options{ThroughputStatePath: path}, upstreamrequest.NewChannel())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(m.throughput) != 0 || m.lastThroughput.IsZero() {
		t.Fatal("unknown historical protocol must not affect HTTP selection", m.throughput, m.lastThroughput)
	}
}
