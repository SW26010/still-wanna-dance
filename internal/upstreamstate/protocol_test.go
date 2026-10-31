package upstreamstate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestHTTPSRecoveryDoesNotUseHTTPThroughput(t *testing.T) {
	m, f := candidateMonitor(t)
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.mu.Lock()
	for _, c := range f.Current(entry(Resource, "play.udon.dance")) {
		m.record(observation{op: Resource, route: "play.udon.dance", channel: c.ID, scheme: "http", state: "available", at: now, songID: 42, latency: time.Millisecond, bytes: 1000, transferDuration: time.Millisecond})
	}
	m.throughputStatePath = filepath.Join(t.TempDir(), "throughput.json")
	if err := m.saveThroughputTime(); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	m.record(observation{op: Resource, route: "play.udon.dance", channel: "a/play.udon.dance", scheme: "https", state: "available", at: now.Add(time.Millisecond), songID: 42, latency: time.Second})
	m.mu.Unlock()
	s, ok := m.Recommended(Resource)
	if !ok || s.Channel.ID != "a/play.udon.dance" || s.Result.EstimatedSpeedBPS != nil || s.Result.LastThroughput.Scheme != "http" {
		t.Fatal(s, ok)
	}
	restored, err := newMonitor(Options{ThroughputStatePath: m.throughputStatePath}, upstreamrequest.NewChannel())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, o := range restored.throughput {
		if o.scheme != "http" {
			t.Fatal("lost protocol", o)
		}
	}
}
