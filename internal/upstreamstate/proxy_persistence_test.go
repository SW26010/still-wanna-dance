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

func TestDirectMonitorIgnoresObsoleteProxySamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	at := time.Now().Add(-time.Minute)
	a := "stable-v1-" + strings.Repeat("a", 64)

	sample := ThroughputSample{ObservedAt: at, SongID: 42, Bytes: 16 << 20, Duration: 2 * time.Second}
	key := func(id string) string { return "resource/play.udon.dance/socks5/play.udon.dance/" + id }
	state := throughputState{SchemaVersion: throughputSchemaVersion, LastAttempt: at, Samples: map[string]ThroughputSample{key(a): sample, key("1"): sample, key("2"): sample}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := newMonitor(Options{ThroughputStatePath: path}, upstreamrequest.NewChannel())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(m.throughput) != 0 || !m.lastThroughput.Equal(at) {
		t.Fatal("restored obsolete proxy measurements or reset cooldown", m.throughput, m.lastThroughput)
	}
}
