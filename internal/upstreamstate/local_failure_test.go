package upstreamstate

import (
	"testing"
	"time"
)

func TestLocalFailuresPreserveRecommendationSamplesAndSources(t *testing.T) {
	m, f := candidateMonitor(t)
	now := time.Now().Add(-time.Second)
	m.mu.Lock()
	for _, id := range []string{"a", "b"} {
		bytes := int64(1000)
		if id == "a" {
			bytes = 4000
		}
		recordFixture(m, observation{op: Resource, route: "play.udon.dance", channel: id + "/play.udon.dance", state: "available", at: now, latency: time.Millisecond, duration: time.Second, transferDuration: time.Second, bytes: bytes, songID: 42})
	}
	m.updatePreferencesLocked(f, now, "")
	m.mu.Unlock()
	before, ok := m.Recommended(Resource)
	if !ok {
		t.Fatal("missing baseline recommendation")
	}
	for _, state := range []string{"local_capacity", "runtime_retired", "channel_unavailable", "canceled"} {
		m.mu.Lock()
		o := observation{op: Resource, route: "play.udon.dance", channel: before.Channel.ID, state: state, at: time.Now(), songID: 42}
		m.record(o)
		m.updatePreferencesLocked(f, o.at, "")
		m.mu.Unlock()
		after, ok := m.Recommended(Resource)
		if !ok || after.Channel.ID != before.Channel.ID || after.Result.Samples != before.Result.Samples || after.Result.ObservedAt != before.Result.ObservedAt || after.Result.LocalDiagnostic != state || after.Result.LastThroughput == nil || after.Result.LastThroughput.ObservedAt != now {
			t.Fatalf("local error contaminated quality: %+v", after.Result)
		}
		m.mu.Lock()
		status := m.throughputStatus(Status{Scheduled: true, Results: []Result{after.Result}}, time.Now())
		m.mu.Unlock()
		if status != state {
			t.Fatal("local throughput attempt hidden", status)
		}
	}
	// Real business timeouts still remove eligibility and preserve historical
	// throughput only as history, not a new successful measurement.
	m.mu.Lock()
	m.record(observation{op: Resource, route: "play.udon.dance", channel: before.Channel.ID, state: "timeout", at: time.Now()})
	m.mu.Unlock()
	after, ok := m.Recommended(Resource)
	if !ok || after.Channel.ID == before.Channel.ID {
		t.Fatal("real timeout ignored")
	}
}

func TestLocalPlaybackFailureKeepsResourceDiscovery(t *testing.T) {
	m := testMonitor(t, fixture)
	prior := observation{op: PlaybackURL, route: "cf", state: "available", resourceHost: "play.udon.dance", at: time.Now().Add(-time.Second), songID: 42}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(prior)
	for _, state := range []string{"local_capacity", "channel_unavailable"} {
		m.record(observation{op: PlaybackURL, route: "cf", state: state, at: time.Now()})
		if got := m.resourceSources["playback_url/cf"]; got.resourceHost != prior.resourceHost || got.at != prior.at {
			t.Fatal("local failure erased discovery", state)
		}
	}
	if r := m.resultLocked(PlaybackURL, "cf", time.Now().Add(m.policy.Lifetime+time.Second)); r.State != "stale" {
		t.Fatal("local diagnostic renewed sample")
	}
}
