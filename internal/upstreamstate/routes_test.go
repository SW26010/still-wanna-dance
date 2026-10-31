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
		{op: PlaybackURL, route: "cf", state: "available", at: now, duration: 10 * time.Millisecond, songID: 42},
		{op: Resource, route: "play.udon.dance", state: "upstream_error", http: 503, stage: "headers", at: now, songID: 42},
		{op: Resource, route: "nya.xin.moe", state: "available", at: now, duration: time.Second, bytes: 65536, songID: 42},
	} {
		recordFixture(m, o)
	}
	m.mu.Unlock()
	playback, resources := m.Results(PlaybackURL), m.Results(Resource)
	if len(playback) != 1 || len(resources) != 2 {
		t.Fatal(playback, resources)
	}
	if playback[0].State != "available" {
		t.Fatal(playback)
	}
	if resources[0].HTTP != 503 || resources[0].Reason != "upstream_error" || resources[1].State != "available" {
		t.Fatal(resources)
	}
	for _, r := range []Result{resources[0]} {
		if r.State != "unavailable" || r.Entry == "" || r.EstimatedLatencyMS != nil || r.EstimatedSpeedBPS != nil {
			t.Fatal(r)
		}
	}
	// When both fail, neither route's reason or HTTP code is hidden by aggregation.
	m.mu.Lock()
	recordFixture(m, observation{op: Resource, route: "nya.xin.moe", state: "timeout", stage: "body", at: now.Add(time.Millisecond), songID: 42})
	m.mu.Unlock()
	resources = m.Results(Resource)
	if resources[0].HTTP != 503 || resources[1].Reason != "timeout" || resources[1].Stage != "body" {
		t.Fatal(resources)
	}
	if resultFor(t, m, PlaybackURL, "cf").State != "available" {
		t.Fatal("failure leaked across operations")
	}
	expired := testMonitorWithPolicy(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected I/O")
		return nil, nil
	}, Policy{Lifetime: time.Nanosecond, FailureLifetime: time.Nanosecond})
	for _, history := range m.history {
		o := history[len(history)-1]
		o.at = time.Now().Add(-time.Second)
		recordFixture(expired, o)
	}
	for _, r := range expired.Snapshot().Results {
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
		recordFixture(m, observation{op: PlaybackURL, route: "cf", state: "available", duration: d, latency: d, at: at, songID: id})
		m.mu.Unlock()
	}
	add(100*time.Millisecond, now, 42)
	for range 9 {
		add(50*time.Millisecond, now, 42)
	}
	before := resultFor(t, m, PlaybackURL, "cf")
	if before.Samples != 8 || *before.ProbeDurationMS != 50 {
		t.Fatal(before)
	}
	m.mu.Lock()
	recordFixture(m, observation{op: PlaybackURL, route: "cf", state: "timeout", at: now.Add(-time.Second)})
	m.mu.Unlock()
	if !reflect.DeepEqual(before, resultFor(t, m, PlaybackURL, "cf")) {
		t.Fatal("accepted old result")
	}
	add(80*time.Millisecond, now, 43)
	if r := resultFor(t, m, PlaybackURL, "cf"); r.Samples != 1 || r.SampleSongID != 43 || *r.ProbeDurationMS != 80 {
		t.Fatal(r)
	}
	if r := resultFor(t, m, Catalog, "api"); r.State != "unknown" {
		t.Fatal("history leaked across routes", r)
	}
}

func TestResultsIndependentOfReadsAndCompletionOrder(t *testing.T) {
	now := time.Now()
	var expected []Result
	for _, first := range []string{"play.udon.dance", "nya.xin.moe"} {
		for _, read := range []bool{false, true} {
			m := testMonitor(t, func(*http.Request) (*http.Response, error) { t.Fatal("read triggered I/O"); return nil, nil })
			second := "nya.xin.moe"
			if first == "nya.xin.moe" {
				second = "play.udon.dance"
			}
			m.resourceSources = map[string]observation{
				"fixture/play.udon.dance": {resourceHost: "play.udon.dance", at: now},
				"fixture/nya.xin.moe":     {resourceHost: "nya.xin.moe", at: now},
			}
			for i, route := range []string{first, second} {
				d := 100 * time.Millisecond
				if route == "nya.xin.moe" {
					d = 95 * time.Millisecond
				}
				m.mu.Lock()
				recordFixture(m, observation{op: Resource, route: route, state: "available", duration: d, latency: d, at: now, songID: 42})
				m.mu.Unlock()
				if i == 0 && read {
					for range 10 {
						m.Results(Resource)
						m.Snapshot()
					}
					if r := resultFor(t, m, Resource, second); r.State != "unknown" || r.Entry == "" {
						t.Fatal("missing pending route", r)
					}
				}
			}
			got := m.Results(Resource)
			if len(got) != 2 || got[0].Route != "play.udon.dance" || got[1].Route != "nya.xin.moe" || *got[0].ProbeDurationMS != 100 || *got[1].ProbeDurationMS != 95 {
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
