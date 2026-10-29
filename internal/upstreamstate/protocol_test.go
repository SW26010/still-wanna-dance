package upstreamstate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"still-wanna-dance/internal/upstreamrequest"
	"strings"
	"testing"
	"time"
)

func TestProbeProtocolFallbackPreservesEvidence(t *testing.T) {
	var calls []string
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme)
		if r.URL.Scheme == "https" {
			return nil, io.EOF
		}
		return fixture(r)
	}))
	for _, route := range routeIDs(Catalog) {
		o, id, _ := probeCatalogData(context.Background(), client, DefaultPolicy(), route)
		if o.state != "available" || o.scheme != "http" || id <= 0 {
			t.Fatal(o, id)
		}
	}
	o, sample, result := probePlaybackResponse(context.Background(), client, DefaultPolicy(), 42, playbackRoutes[0])
	if o.state != "available" || o.scheme != "http" || sample == nil || result.API != apiBase+"/Api/Songs/play" || result.URL != videoFixture {
		t.Fatal(o, sample, result)
	}
	o = probeResourceMode(context.Background(), client, DefaultPolicy(), 42, *sample, false)
	if o.state != "available" || o.scheme != "http" {
		t.Fatal(o)
	}
	if strings.Join(calls, ",") != "https,http,https,http,https,http,https,http,https,http" {
		t.Fatal(calls)
	}
}

func TestResourceProbeRetriesProtocolAtRedirectTarget(t *testing.T) {
	var calls []string
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
		if r.URL.Scheme == "https" {
			return nil, io.EOF
		}
		if r.URL.Query().Get("next") == "" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {strings.Replace(videoFixture, "https://", "http://", 1) + "&next=1"}}, Body: http.NoBody, Request: r}, nil
		}
		return fixture(r)
	}))
	sample, _ := parseSample(videoFixture)
	o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
	if o.state != "available" || o.scheme != "http" || strings.Join(calls, ",") != "https/,http/,https/1,http/1" {
		t.Fatal(o, calls)
	}
}

func TestResourceProbeProtocolRedirectsKeepEndpointBudget(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls []string
			client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.Scheme)
				if r.URL.Scheme == "https" {
					return nil, io.EOF
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {videoFixture}}, Body: http.NoBody, Request: r}, nil
			}))
			sample, _ := parseSample(videoFixture)
			o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
			if o.state == "available" || strings.Join(calls, ",") != "https,http,https" {
				t.Fatal(o, calls)
			}
		})
	}
	t.Run("revisited endpoint", func(t *testing.T) {
		var calls []string
		client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
			if r.URL.Scheme == "https" {
				return nil, io.EOF
			}
			location := videoFixture
			if r.URL.Query().Get("next") == "" {
				location += "&next=1"
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: r}, nil
		}))
		sample, _ := parseSample(videoFixture)
		o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
		if o.state != "invalid" || strings.Join(calls, ",") != "https/,http/,https/1,http/1" {
			t.Fatal(o, calls)
		}
	})
}

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
