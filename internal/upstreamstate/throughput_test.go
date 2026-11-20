package upstreamstate

import (
	"still-wanna-dance/internal/upstreamrequest"
	"testing"
	"time"
)

func TestConfigurableThroughputInterval(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		configured, effective time.Duration
	}{
		{"default", 0, 20 * time.Minute},
		{"shorter", 5 * time.Minute, 5 * time.Minute},
		{"longer", 30 * time.Minute, 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testMonitorWithPolicy(t, fixture, Policy{ThroughputInterval: tc.configured})

			s := m.Snapshot()
			if s.Policy.ThroughputInterval != tc.effective {
				t.Fatal("unexpected effective throughput interval", s)
			}
		})
	}
	if _, err := NewMonitor(Options{Channel: upstreamrequest.NewChannel(), Policy: Policy{ThroughputInterval: -time.Minute}}); err == nil {
		t.Fatal("negative interval accepted")
	}
}

func TestLatencyDoesNotReplaceThroughput(t *testing.T) {
	m := testMonitor(t, fixture)
	now := time.Now()
	recordFixture(m, observation{op: Resource, route: "play.udon.dance", state: "available", at: now, songID: 42, bytes: 16 << 20, transferDuration: 2 * time.Second, duration: 3 * time.Second, latency: time.Second})
	recordFixture(m, observation{op: Resource, route: "play.udon.dance", state: "available", at: now.Add(time.Second), songID: 73, duration: time.Millisecond, latency: time.Millisecond})
	r := fixtureResult(m, Resource, "play.udon.dance", now.Add(2*time.Second))
	if r.EstimatedSpeedBPS == nil || *r.EstimatedSpeedBPS != 8<<20 || !r.ThroughputObservedAt.Equal(now) || r.ThroughputSongID != 42 || r.SampleSongID != 73 {
		t.Fatal(r)
	}
}
