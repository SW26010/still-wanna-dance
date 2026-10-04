package upstreamstate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestAutomaticThroughputCooldownSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	var full, light atomic.Int32
	ch := upstreamrequest.NewChannel()
	ch.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Header.Get("Range") {
		case "":
		case "bytes=0-0":
			light.Add(1)
		default:
			full.Add(1)
			time.Sleep(time.Millisecond)
		}
		return fixture(r)
	}))
	create := func() *Monitor {
		m, err := newMonitor(Options{ThroughputStatePath: path}, ch)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m
	}
	check := func(m *Monitor) {
		t.Helper()
		if err := m.checkRequest(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	m := create()
	check(m)
	if full.Load() != 2 {
		t.Fatal("initial throughput", full.Load())
	}
	check(m)
	if full.Load() != 2 || light.Load() != 2 {
		t.Fatal("cooldown", full.Load(), light.Load())
	}
	m.Close()
	restarted := create()
	check(restarted)
	if full.Load() != 2 || light.Load() != 4 {
		t.Fatal("restart bypassed cooldown")
	}
	for _, r := range restarted.Results(Resource) {
		if r.State != "available" || r.EstimatedLatencyMS == nil || r.EstimatedSpeedBPS == nil || r.LastThroughput == nil || r.LastThroughput.SongID != 42 {
			t.Fatalf("persisted throughput not restored: %+v", r)
		}
	}
	restarted.Close()
	b, _ := json.Marshal(time.Now().Add(-16 * time.Minute))
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	check(create())
	if full.Load() != 4 {
		t.Fatal("overdue throughput not run")
	}
}

func TestManualThroughputResetsPersistedCooldown(t *testing.T) {
	for _, kind := range []CheckKind{CheckCatalog, CheckPlayback, CheckLatency, CheckThroughput, ""} {
		t.Run(string(kind), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "throughput.json")
			old := time.Now().Add(-14 * time.Minute)
			b, _ := json.Marshal(old)
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			ch := upstreamrequest.NewChannel()
			var firstTransfer time.Time
			var full atomic.Int32
			ch.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
				if rg := r.Header.Get("Range"); rg != "" && rg != "bytes=0-0" {
					full.Add(1)
					data, err := os.ReadFile(path)
					if err != nil {
						t.Error(err)
					}
					var state throughputState
					if err := json.Unmarshal(data, &state); err != nil {
						t.Error(err)
					}
					saved := state.LastAttempt
					if !saved.After(old) {
						t.Error("timer not persisted before transfer")
					}
					if firstTransfer.IsZero() {
						firstTransfer = saved
					} else if !firstTransfer.Equal(saved) {
						t.Error("timer reset for each channel")
					}
				}
				return fixture(r)
			}))
			m, err := newMonitor(Options{ThroughputStatePath: path}, ch)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if kind == "" {
				err = m.Check(context.Background())
			} else {
				err = m.CheckSelected(context.Background(), kind)
			}
			if err != nil {
				t.Fatal(err)
			}
			m.Close()
			restarted, err := newMonitor(Options{ThroughputStatePath: path}, ch)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if kind == CheckThroughput || kind == "" {
				if full.Load() != 2 || !restarted.lastThroughput.Equal(firstTransfer) {
					t.Fatal("manual cooldown not retained")
				}
				if err := restarted.checkRequest(context.Background(), false); err != nil {
					t.Fatal(err)
				}
				if full.Load() != 2 {
					t.Fatal("automatic throughput ran during cooldown")
				}
			} else if !restarted.lastThroughput.Equal(old) {
				t.Fatal("non-throughput check changed timer")
			}
		})
	}
}

func TestHistoricalThroughputSurvivesRestartWithoutRevivingHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	ch := upstreamrequest.NewChannel()
	ch.Publish(transportFunc(fixture))
	m, err := newMonitor(Options{ThroughputStatePath: path}, ch)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	m.lastThroughput = at
	m.record(observation{op: Resource, route: "cf", state: "available", at: at, songID: 42, bytes: 16 << 20, transferDuration: 2 * time.Second})
	m.Close()
	restored, err := newMonitor(Options{ThroughputStatePath: path}, ch)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	r := restored.Results(Resource)[0]
	if r.State != "unknown" || r.EstimatedSpeedBPS != nil || r.LastThroughput == nil || r.LastThroughput.SongID != 42 || !r.LastThroughput.ObservedAt.Equal(at) || r.LastThroughput.Bytes != 16<<20 || r.LastThroughput.Duration != 2*time.Second {
		t.Fatalf("history not restored independently of health: %+v", r)
	}
	// Fresh failures and light checks must not replace historical ownership.
	restored.record(observation{op: Resource, route: "cf", state: "network_error", at: time.Now(), songID: 73})
	if r = restored.Results(Resource)[0]; r.State != "unavailable" || r.LastThroughput.SongID != 42 {
		t.Fatal(r)
	}
	s := restored.Snapshot()
	if !s.NextThroughput.Equal(at.Add(15 * time.Minute)) {
		t.Fatal(s.NextThroughput)
	}
}

func TestThroughputWaitingReasons(t *testing.T) {
	m := testMonitor(t, fixture)
	now := time.Now()
	for _, tc := range []struct {
		status Status
		want   string
	}{
		{Status{Closed: true}, "closed"},
		{Status{ResourcesPaused: true}, "business_busy"},
		{Status{}, "not_scheduled"},
		{Status{Scheduled: true, NextThroughput: now.Add(time.Minute)}, "cooldown"},
		{Status{Scheduled: true, Checking: true}, "preparing"},
		{Status{Scheduled: true}, "waiting_sample"},
		{Status{Scheduled: true, Results: []Result{{Operation: PlaybackURL, State: "available"}}}, "due"},
	} {
		if got := m.throughputStatus(tc.status, now); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestLatencyDoesNotReplaceThroughput(t *testing.T) {
	m := testMonitor(t, fixture)
	now := time.Now()
	m.record(observation{op: Resource, route: "cf", state: "available", at: now, songID: 42, bytes: 16 << 20, transferDuration: 2 * time.Second, duration: 3 * time.Second, latency: time.Second})
	m.record(observation{op: Resource, route: "cf", state: "available", at: now.Add(time.Second), songID: 73, duration: time.Millisecond, latency: time.Millisecond})
	r := m.resultLocked(Resource, "cf", now.Add(2*time.Second))
	if r.EstimatedSpeedBPS == nil || *r.EstimatedSpeedBPS != 8<<20 || !r.ThroughputObservedAt.Equal(now) || r.ThroughputSongID != 42 || r.SampleSongID != 73 {
		t.Fatal(r)
	}
}

func TestLatencyProbeReadsOnlyOneByte(t *testing.T) {
	for _, status := range []int{206, 200} {
		body := strings.NewReader("body must not be downloaded")
		client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Range") != "bytes=0-0" {
				t.Fatal(r.Header)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Range": []string{"bytes 0-0/131072"}}, Body: io.NopCloser(body)}, nil
		}))
		sample, err := parseSample(videoFixture)
		if err != nil {
			t.Fatal(err)
		}
		o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, videoRoutes[0], sample, false)
		wantRead := 0
		if status == 206 {
			wantRead = 1
			if o.state != "available" {
				t.Fatal(o)
			}
		} else if o.state == "available" {
			t.Fatal("accepted ignored Range")
		}
		if body.Size()-int64(body.Len()) != int64(wantRead) || o.bytes != 0 || o.transferDuration != 0 {
			t.Fatal("latency probe read or measured throughput", o)
		}
	}
}
