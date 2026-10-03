package upstreamstate

import (
	"net/http"
	"testing"
	"time"
)

func TestOperationIsolationHistoryAndExpiry(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected I/O"); return nil, nil })
	now := time.Now()
	m.mu.Lock()
	for _, o := range []observation{
		{op: Catalog, route: "api", state: "available", at: now, latency: 10 * time.Millisecond, duration: time.Second, bytes: 100},
		{op: PlaybackURL, route: "cf", state: "available", at: now, latency: time.Millisecond, duration: 10 * time.Millisecond, songID: 42},
		{op: PlaybackURL, route: "hkg", state: "available", at: now, latency: 2 * time.Millisecond, duration: 20 * time.Millisecond, songID: 42},
		{op: Resource, route: "cf", state: "available", at: now, latency: time.Millisecond, duration: time.Second, bytes: 65536, songID: 42},
		{op: Resource, route: "hkg", state: "available", at: now, latency: time.Millisecond, duration: 100 * time.Millisecond, bytes: 65536, songID: 42},
	} {
		m.record(o)
	}
	m.mu.Unlock()
	if m.Best(PlaybackURL).Route != "cf" || m.Best(Resource).Route != "hkg" {
		t.Fatal("shared score between operations")
	}
	m.mu.Lock()
	m.record(observation{op: Resource, route: "hkg", state: "timeout", at: now.Add(time.Millisecond)})
	m.mu.Unlock()
	if m.Best(Resource).Route != "cf" || m.Best(PlaybackURL).Route != "cf" {
		t.Fatal("failure leaked or no fallback")
	}
	p := DefaultPolicy()
	p.Lifetime = time.Nanosecond
	p.FailureLifetime = time.Nanosecond
	m.SetPolicy(p)
	time.Sleep(2 * time.Millisecond)
	for _, op := range operations {
		if r := m.Best(op); r.State != "stale" || r.Entry != "" || r.EstimatedLatencyMS != nil {
			t.Fatal(r)
		}
	}
}

func TestBoundedHistoryHysteresisAndLateResults(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { return nil, nil })
	now := time.Now()
	add := func(route string, d time.Duration, at time.Time) {
		m.mu.Lock()
		m.record(observation{op: PlaybackURL, route: route, state: "available", duration: d, latency: d, at: at, songID: 42})
		m.mu.Unlock()
	}
	add("cf", 100*time.Millisecond, now)
	add("hkg", 110*time.Millisecond, now)
	m.mu.Lock()
	m.commitPreferencesLocked(now)
	m.mu.Unlock()
	if m.Best(PlaybackURL).Route != "cf" {
		t.Fatal("initial choice")
	}
	for range 9 {
		add("hkg", 95*time.Millisecond, now)
	}
	if m.Best(PlaybackURL).Route != "cf" {
		t.Fatal("jitter")
	}
	for range 9 {
		add("hkg", 50*time.Millisecond, now)
	}
	if r := m.Best(PlaybackURL); r.Route != "hkg" || r.Samples != 8 {
		t.Fatal(r)
	}
	m.mu.Lock()
	m.record(observation{op: PlaybackURL, route: "hkg", state: "timeout", at: now.Add(-time.Second)})
	m.mu.Unlock()
	if m.Best(PlaybackURL).Route != "hkg" {
		t.Fatal("accepted old result")
	}
}

func TestRecommendationIndependentOfReadsAndCompletionOrder(t *testing.T) {
	for _, first := range []string{"cf", "hkg"} {
		for _, read := range []bool{false, true} {
			m := testMonitor(t, func(*http.Request) (*http.Response, error) {
				t.Fatal("read triggered I/O")
				return nil, nil
			})
			now := time.Now()
			second := "hkg"
			if first == "hkg" {
				second = "cf"
			}
			for i, route := range []string{first, second} {
				d := 100 * time.Millisecond
				if route == "hkg" {
					d = 95 * time.Millisecond
				}
				m.mu.Lock()
				m.record(observation{op: PlaybackURL, route: route, state: "available", duration: d, latency: d, at: now, songID: 42})
				m.mu.Unlock()
				if read && i == 0 {
					for range 10 {
						m.Best(PlaybackURL)
						m.Snapshot()
					}
					if len(m.preferred) != 0 {
						t.Fatal("partial read committed a preference")
					}
				}
			}
			m.mu.Lock()
			m.commitPreferencesLocked(now)
			m.mu.Unlock()
			if got := m.Best(PlaybackURL); got.Route != "hkg" || m.preferred[PlaybackURL] != "hkg" {
				t.Fatalf("first=%s read=%v result=%+v preference=%s", first, read, got, m.preferred[PlaybackURL])
			}
			// Reading after expiry does not clear an anchor. Publishing the next
			// completed batch clears it when no valid recommendation remains.
			m.mu.Lock()
			later := now.Add(m.policy.Lifetime + time.Second)
			r := m.bestLocked(PlaybackURL, later)
			retained := m.preferred[PlaybackURL]
			m.commitPreferencesLocked(later)
			cleared := m.preferred[PlaybackURL] == ""
			m.mu.Unlock()
			if r.State != "stale" || retained != "hkg" || !cleared {
				t.Fatal("read or commit changed expiry semantics", r, retained, cleared)
			}
		}
	}
}
