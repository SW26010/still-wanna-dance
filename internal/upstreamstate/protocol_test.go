package upstreamstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"still-wanna-dance/internal/upstreamrequest"
	"testing"
	"time"
)

func TestRestoredThroughputRequiresExplicitHTTP(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, scheme := range []string{"http", "https", ""} {
		t.Run("scheme_"+scheme, func(t *testing.T) {
			key := "resource/play.udon.dance/direct/play.udon.dance/192.0.2.1"
			data, err := json.Marshal(throughputState{SchemaVersion: throughputSchemaVersion, LastAttempt: at, Samples: map[string]ThroughputSample{key: {Scheme: scheme, ObservedAt: at, SongID: 42, Bytes: 1000, Duration: time.Second}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "throughput.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			m, err := newMonitor(Options{ThroughputStatePath: path}, upstreamrequest.NewChannel())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			_, ok := m.throughput[key]
			if ok != (scheme == "http") || !m.lastThroughput.Equal(at) || len(m.Results(Resource)) != 0 {
				t.Fatal("historical sample changed HTTP health or cooldown", m.throughput, m.lastThroughput)
			}
		})
	}
}
