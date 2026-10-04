package upstreamstate

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestOperationAndRouteIsolation(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected I/O"); return nil, nil })
	now := time.Now()
	m.mu.Lock()
	for _, o := range []observation{
		{op: Catalog, route: "api", state: "available", at: now, duration: time.Second, bytes: 100},
		{op: Catalog, route: "kiva", state: "available", at: now, duration: time.Second, bytes: 120},
		{op: Catalog, route: "wanna", state: "available", at: now, duration: time.Second, bytes: 120},
		{op: PlaybackURL, route: "cf", state: "available", at: now, duration: 10 * time.Millisecond, songID: 42},
		{op: PlaybackURL, route: "hkg", state: "timeout", stage: "headers", at: now, songID: 42},
		{op: Resource, route: "cf", state: "upstream_error", http: 503, stage: "headers", at: now, songID: 42},
		{op: Resource, route: "hkg", state: "available", at: now, duration: time.Second, bytes: 65536, songID: 42},
	} {
		m.record(o)
	}
	m.mu.Unlock()
	playback, resources := m.Results(PlaybackURL), m.Results(Resource)
	if len(playback) != 2 || len(resources) != 2 {
		t.Fatal(playback, resources)
	}
	if playback[0].State != "available" || playback[1].Reason != "timeout" || playback[1].Stage != "headers" {
		t.Fatal(playback)
	}
	if resources[0].HTTP != 503 || resources[0].Reason != "upstream_error" || resources[1].State != "available" {
		t.Fatal(resources)
	}
	for _, r := range []Result{playback[1], resources[0]} {
		if r.State != "unavailable" || r.Entry == "" || r.EstimatedLatencyMS != nil || r.EstimatedSpeedBPS != nil {
			t.Fatal(r)
		}
	}
	// When both fail, neither route's reason or HTTP code is hidden by aggregation.
	m.mu.Lock()
	m.record(observation{op: Resource, route: "hkg", state: "timeout", stage: "body", at: now.Add(time.Millisecond), songID: 42})
	m.mu.Unlock()
	resources = m.Results(Resource)
	if resources[0].HTTP != 503 || resources[1].Reason != "timeout" || resources[1].Stage != "body" {
		t.Fatal(resources)
	}
	if resultFor(t, m, PlaybackURL, "cf").State != "available" {
		t.Fatal("failure leaked across operations")
	}
	p := DefaultPolicy()
	p.Lifetime = time.Nanosecond
	p.FailureLifetime = time.Nanosecond
	if err := m.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	for _, r := range m.Snapshot().Results {
		if r.State != "stale" || r.Entry == "" || r.Route == "" || r.EstimatedLatencyMS != nil {
			t.Fatal(r)
		}
	}
	m.Close()
	for _, r := range m.Snapshot().Results {
		if r.State != "closed" || r.Route == "" || r.Entry == "" || r.EstimatedSpeedBPS != nil {
			t.Fatal(r)
		}
	}
}

func TestBoundedHistoryAndLateResults(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { return nil, nil })
	now := time.Now()
	add := func(d time.Duration, at time.Time, id int64) {
		m.mu.Lock()
		m.record(observation{op: PlaybackURL, route: "hkg", state: "available", duration: d, latency: d, at: at, songID: id})
		m.mu.Unlock()
	}
	add(100*time.Millisecond, now, 42)
	for range 9 {
		add(50*time.Millisecond, now, 42)
	}
	before := resultFor(t, m, PlaybackURL, "hkg")
	if before.Samples != 8 || *before.ProbeDurationMS != 50 {
		t.Fatal(before)
	}
	m.mu.Lock()
	m.record(observation{op: PlaybackURL, route: "hkg", state: "timeout", at: now.Add(-time.Second)})
	m.mu.Unlock()
	if !reflect.DeepEqual(before, resultFor(t, m, PlaybackURL, "hkg")) {
		t.Fatal("accepted old result")
	}
	add(80*time.Millisecond, now, 43)
	if r := resultFor(t, m, PlaybackURL, "hkg"); r.Samples != 1 || r.SampleSongID != 43 || *r.ProbeDurationMS != 80 {
		t.Fatal(r)
	}
	if r := resultFor(t, m, PlaybackURL, "cf"); r.State != "unknown" {
		t.Fatal("history leaked across routes", r)
	}
}

func TestResultsIndependentOfReadsAndCompletionOrder(t *testing.T) {
	now := time.Now()
	var expected []Result
	for _, first := range []string{"cf", "hkg"} {
		for _, read := range []bool{false, true} {
			m := testMonitor(t, func(*http.Request) (*http.Response, error) { t.Fatal("read triggered I/O"); return nil, nil })
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
				if i == 0 && read {
					for range 10 {
						m.Results(PlaybackURL)
						m.Snapshot()
					}
					if r := resultFor(t, m, PlaybackURL, second); r.State != "unknown" || r.Entry == "" {
						t.Fatal("missing pending route", r)
					}
				}
			}
			got := m.Results(PlaybackURL)
			if len(got) != 2 || got[0].Route != "cf" || got[1].Route != "hkg" || *got[0].ProbeDurationMS != 100 || *got[1].ProbeDurationMS != 95 {
				t.Fatal(got)
			}
			if expected == nil {
				expected = got
			} else if !reflect.DeepEqual(expected, got) {
				t.Fatal("reads or completion order changed results")
			}
		}
	}
}
