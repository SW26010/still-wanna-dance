package upstreamstate

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestSelectedChecksScopeAndBusinessPriority(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		for _, kind := range []CheckKind{CheckCatalog, CheckPlayback, CheckLatency, CheckThroughput} {
			t.Run(string(kind)+map[bool]string{false: "/ordinary", true: "/candidates"}[candidates], func(t *testing.T) {
				var catalogs, playbacks, light, full, active atomic.Int32
				tr := transportFunc(func(r *http.Request) (*http.Response, error) {
					resp, err := fixture(r)
					if rg := r.Header.Get("Range"); rg != "" {
						if active.Add(1) != 1 {
							t.Error("parallel resource transfers")
						}
						if rg == "bytes=0-0" {
							light.Add(1)
						} else {
							full.Add(1)
						}
						resp.Body = measuredBody{resp.Body, &active}
					} else if r.URL.Path == "/Api/Songs/play" {
						playbacks.Add(1)
					} else {
						catalogs.Add(1)
					}
					return resp, err
				})
				ch := upstreamrequest.NewChannel()
				multiplier := int32(1)
				if candidates {
					multiplier = 2
					ch.Publish(serialProbeChannels{&candidateFixture{expires: time.Now().Add(time.Hour), second: true, changed: make(chan struct{})}, tr})
				} else {
					ch.Publish(tr)
				}
				m, err := newMonitor(Options{}, ch)
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				m.songID, m.songAt = 42, time.Now()
				m.lastThroughput = time.Now() // Automatic throughput is still cooling down.
				release := ch.BeginResourceLoad()
				defer release()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := m.CheckSelected(ctx, kind); err != nil {
					t.Fatal(err)
				}
				if kind == CheckCatalog {
					if catalogs.Load() != 3*multiplier || playbacks.Load() != 0 {
						t.Fatal("wrong catalog scope")
					}
				} else if catalogs.Load() != 0 || playbacks.Load() != 2*multiplier {
					t.Fatal("unexpected prerequisite work", catalogs.Load(), playbacks.Load())
				}
				wantLight, wantFull := int32(0), int32(0)
				if kind == CheckLatency {
					wantLight = 2 * multiplier
				}
				if kind == CheckThroughput {
					wantFull = 2 * multiplier
				}
				if light.Load() != wantLight || full.Load() != wantFull || active.Load() != 0 {
					t.Fatal("wrong resource checks", light.Load(), full.Load(), active.Load())
				}
				for _, op := range operations {
					for _, r := range m.Results(op) {
						if kind.includes(op) && r.State != "available" {
							t.Fatal("missing selected result", r)
						}
						if !kind.includes(op) && !r.ObservedAt.IsZero() {
							t.Fatal("unrelated result replaced", r)
						}
					}
				}
			})
		}
	}
}

func TestSelectedCheckPreservesUnrelatedPreferences(t *testing.T) {
	for _, kind := range []CheckKind{CheckCatalog, CheckPlayback, CheckLatency, CheckThroughput} {
		t.Run(string(kind), func(t *testing.T) {
			m, provider := candidateMonitor(t)
			at := time.Now().Add(-time.Minute)
			m.songID, m.songAt = 42, at
			m.preferences = make(map[Operation]*preference)
			before := make(map[Operation]preference)
			for _, op := range operations {
				route := "play.udon.dance"
				if op != Resource {
					route = routeIDs(op)[0]
				}
				cs := provider.Current(entry(op, route))
				for i, c := range cs {
					recordFixture(m, observation{op: op, route: route, channel: c.ID, state: "available", at: at,
						latency: time.Duration(2-i) * time.Millisecond, duration: time.Second, transferDuration: time.Second, bytes: int64(1000 * (i + 1)), songID: 42})
				}
				p := preference{current: route + "/" + cs[0].ID, challenger: route + "/" + cs[1].ID, wins: 1, throughputVote: at}
				m.preferences[op] = &p
				before[op] = p
			}
			if err := m.CheckSelected(context.Background(), kind); err != nil {
				t.Fatal(err)
			}
			for _, op := range operations {
				if !kind.includes(op) {
					if got := m.preferences[op]; got == nil || *got != before[op] {
						t.Fatalf("%s check changed %s preference: got %+v, want %+v", kind, op, got, before[op])
					}
				}
			}
		})
	}
}

func TestSelectedThroughputPreemptsPausedAutomaticBatch(t *testing.T) {
	var active atomic.Int32
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		resp, err := fixture(r)
		if r.Header.Get("Range") != "" {
			active.Add(1)
			resp.Body = measuredBody{resp.Body, &active}
		}
		return resp, err
	})
	release := m.channel.BeginResourceLoad()
	defer release()
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return m.Snapshot().Checking && resultFor(t, m, Catalog, "api").State == "available" })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CheckSelected(ctx, CheckThroughput); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Results(Resource) {
		if r.State != "available" || r.EstimatedSpeedBPS == nil {
			t.Fatal("manual throughput blocked by business", r)
		}
	}
}
