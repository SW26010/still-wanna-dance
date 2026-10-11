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
	for _, document := range []string{
		`{"lastAttempt":"2026-01-01T00:00:00Z"}`,
		`{"schemaVersion":999,"lastAttempt":"2026-01-01T00:00:00Z"}`,
		`{"schemaVersion":1,"lastAttempt":"2099-01-01T00:00:00Z"}`,
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
		m.Close()
	}
}

func TestThroughputUTCAndDurationContract(t *testing.T) {
	m := testMonitor(t, fixture)
	m.throughputStatePath = filepath.Join(t.TempDir(), "throughput.json")
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 8*3600))
	m.lastThroughput = at
	m.throughput["resource/play.udon.dance"] = observation{at: at, bytes: 42, transferDuration: 123456789, songID: 1}
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
	if state.SchemaVersion != 1 || !state.LastAttempt.Equal(at) {
		t.Fatal(state)
	}
}

func TestThroughputV1Golden(t *testing.T) {
	b, err := os.ReadFile("testdata/throughput-v1.json")
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
	o, ok := m.throughput["resource/play.udon.dance"]
	if !ok || o.transferDuration != 123456789 || o.at.UTC().Format(time.RFC3339Nano) != "2026-01-02T03:04:05.123456789Z" {
		t.Fatal(o)
	}
}
