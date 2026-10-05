package upstreamstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestThroughputMigratesNodeKeysToDomainKeys(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	old := ThroughputSample{ObservedAt: at, SongID: 42, Bytes: 100, Duration: time.Second}
	newer := old
	newer.ObservedAt = at.Add(time.Minute)
	newer.SongID = 73
	state := throughputState{LastAttempt: at, Samples: map[string]ThroughputSample{
		"resource/cf":              old,
		"resource/play.udon.dance": newer,
		"resource/hkg/direct/media.future.example/192.0.2.1": old,
		"resource/hkg/socks5/nya.xin.moe/1":                  old,
	}}
	b, err := json.Marshal(state)
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
	if len(m.Results(Resource)) != 0 {
		t.Fatal("persisted history discovered a domain")
	}
	if len(m.throughput) != 2 || m.throughput["resource/play.udon.dance"].songID != 73 || m.throughput["resource/media.future.example/direct/media.future.example/192.0.2.1"].songID != 42 || !m.lastThroughput.Equal(at) {
		t.Fatal("lost historical domain ownership or cooldown", m.throughput, m.lastThroughput)
	}
	if err := m.saveThroughputTime(); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved throughputState
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	for key := range saved.Samples {
		if !restorableThroughputKey(key) || migrateThroughputKey(key) != key {
			t.Fatal("persisted legacy node key", key)
		}
	}
}
