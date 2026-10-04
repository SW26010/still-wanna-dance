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
		if r.State != "available" || r.EstimatedLatencyMS == nil || r.EstimatedSpeedBPS != nil || r.TransferDurationMS != nil {
			t.Fatalf("light probe invented speed: %+v", r)
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
