package upstreamstate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f(r)
	if resp != nil {
		resp.Request = r
	}
	return resp, err
}

const catalogFixture = `{"groups":{"contents":[{"songInfos":[{"id":42}]}]}}`
const videoFixture = "https://play.udon.dance/files/123/42-abc.mp4?e=0123456789abcdef0123456789abcdef&s=131072"

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func testMonitor(t *testing.T, f transportFunc) *Monitor {
	t.Helper()
	channel := upstreamrequest.NewChannel()
	channel.Publish(f)
	m, err := newMonitor(Options{}, channel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func fixture(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/Api/Songs/list" {
		return response(200, catalogFixture), nil
	}
	if r.URL.Path == "/Api/Songs/play" {
		u := videoFixture
		if r.URL.Query().Get("node") == "nya" {
			u = strings.Replace(u, "play.udon.dance", "nya.xin.moe", 1)
		}
		resp := response(302, "")
		resp.Header.Set("Location", u)
		return resp, nil
	}
	resp := response(206, strings.Repeat("x", int(MaxProbeBytes)))
	resp.Header.Set("Content-Range", "bytes 0-65535/131072")
	return resp, nil
}
func TestOperationSpecificBestAndReadOnlySnapshot(t *testing.T) {
	var calls atomic.Int32
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path == "/Api/Songs/play" && r.URL.Query().Get("node") == "nya" {
			time.Sleep(30 * time.Millisecond)
		}
		if r.Header.Get("Range") != "" {
			if r.URL.Host == "play.udon.dance" {
				time.Sleep(35 * time.Millisecond)
			} else {
				time.Sleep(2 * time.Millisecond)
			}
			if r.Header.Get("Range") != "bytes=0-65535" || r.URL.Scheme != "https" {
				t.Error(r)
			}
		}
		return fixture(r)
	})
	if r := m.Best(Resource); r.State != "unknown" || r.Entry != "" {
		t.Fatal(r)
	}
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	cat, resolve, resource := m.Best(Catalog), m.Best(PlaybackURL), m.Best(Resource)
	if cat.State != "available" || cat.Entry != apiBase+"/Api/Songs/list" {
		t.Fatal(cat)
	}
	if resolve.Route != "cf" || resolve.EstimatedSpeedBPS != nil || resolve.SampleSongID != 42 {
		t.Fatal(resolve)
	}
	if resource.Route != "hkg" || resource.EstimatedSpeedBPS == nil || resource.EstimatedLatencyMS == nil {
		t.Fatal(resource)
	}
	m.mu.Lock()
	committed := m.preferred[PlaybackURL] == "cf" && m.preferred[Resource] == "hkg"
	m.mu.Unlock()
	if !committed {
		t.Fatal("completed check did not commit operation preferences")
	}
	if calls.Load() != 5 {
		t.Fatal(calls.Load())
	}
	for range 50 {
		m.Snapshot()
		m.Best(Resource)
	}
	if calls.Load() != 5 {
		t.Fatal("read triggered I/O")
	}
	s := m.Snapshot()
	s.Results[0].Entry = "changed"
	*resource.EstimatedSpeedBPS = -1
	if m.Best(Catalog).Entry == "changed" || *m.Best(Resource).EstimatedSpeedBPS < 0 {
		t.Fatal("snapshot aliases state")
	}
}

func TestCatalogFailureAndSampleReuse(t *testing.T) {
	var failed atomic.Bool
	var plays atomic.Int32
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if failed.Load() && r.URL.Path == "/Api/Songs/list" {
			return response(524, ""), nil
		}
		if r.URL.Path == "/Api/Songs/play" {
			plays.Add(1)
		}
		return fixture(r)
	})
	failed.Store(true)
	m.Check(context.Background())
	if got := m.Best(Catalog); got.State != "unavailable" || got.Reason != "origin_timeout" || got.Entry != "" {
		t.Fatal(got)
	}
	if m.Best(PlaybackURL).State != "unknown" || plays.Load() != 0 {
		t.Fatal("guessed sample")
	}
	failed.Store(false)
	m.Check(context.Background())
	failed.Store(true)
	m.Check(context.Background())
	if m.Best(Catalog).State != "unavailable" || m.Best(PlaybackURL).State != "available" || plays.Load() != 4 {
		t.Fatal("catalog failure poisoned other operations")
	}
	p := DefaultPolicy()
	p.SampleLifetime = time.Nanosecond
	p.Lifetime = time.Nanosecond
	m.SetPolicy(p)
	time.Sleep(time.Millisecond)
	m.Check(context.Background())
	if plays.Load() != 4 || m.Best(Resource).State != "stale" {
		t.Fatal("used expired sample")
	}
}

func TestInvalidPlaybackDoesNotProbeResource(t *testing.T) {
	var resourceCalls atomic.Int32
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/play" {
			resp := response(302, "")
			resp.Header.Set("Location", "https://evil.invalid/video")
			return resp, nil
		}
		if r.Header.Get("Range") != "" {
			resourceCalls.Add(1)
		}
		return fixture(r)
	})
	m.Check(context.Background())
	for _, op := range []Operation{PlaybackURL, Resource} {
		r := m.Best(op)
		if r.State != "unavailable" || r.Entry != "" {
			t.Fatal(r)
		}
	}
	if resourceCalls.Load() != 0 {
		t.Fatal("followed invalid location")
	}
}

func TestSharedCheckCancellationAndClose(t *testing.T) {
	entered := make(chan struct{}, 1)
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Check(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); m.Check(ctx) }()
	}
	cancel()
	wg.Wait()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !m.Snapshot().Checking {
		t.Fatal("canceled shared work")
	}
	m.Close()
	if s := m.Snapshot(); s.Checking || !s.Closed || m.Best(Catalog).State != "closed" {
		t.Fatal("not joined")
	}
	if m.Check(context.Background()) == nil || m.Start() == nil || m.SetPolicy(DefaultPolicy()) == nil {
		t.Fatal("reopened closed checker")
	}
}

func TestRuntimeScheduleAndRequestTimeout(t *testing.T) {
	var calls atomic.Int32
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return response(503, ""), nil })
	m.Start()
	m.Start()
	if !m.Snapshot().Scheduled {
		t.Fatal("scheduler missing from snapshot")
	}
	deadline := time.Now().Add(time.Second)
	for m.Snapshot().Finished.IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p := DefaultPolicy()
	p.Interval = 15 * time.Millisecond
	p.RequestTimeout = 10 * time.Millisecond
	if err := m.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 2 || m.Snapshot().Policy != p {
		t.Fatal("new interval not applied")
	}
	if m.SetPolicy(Policy{Lifetime: -time.Second}) == nil {
		t.Fatal("negative policy")
	}
	m.Close()
	if s := m.Snapshot(); s.Scheduled || !s.Closed || !s.NextCheck.IsZero() {
		t.Fatal("closed schedule")
	}
	n := testMonitor(t, func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	n.SetPolicy(p)
	n.Check(context.Background())
	if r := n.Best(Catalog); r.State != "unavailable" || r.Reason != "timeout" {
		t.Fatal(r)
	}
}

type ownedTransport struct{ closed atomic.Bool }

func (t *ownedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fixture(r) }
func (t *ownedTransport) CloseIdleConnections()                             { t.closed.Store(true) }
func TestTransportOwnershipAndNoConfigurationLeaks(t *testing.T) {
	tr := &ownedTransport{}
	channel := upstreamrequest.NewChannel()
	channel.Publish(tr)
	m, err := newMonitor(Options{}, channel)
	if err != nil {
		t.Fatal(err)
	}
	m.Check(context.Background())
	m.Close()
	if tr.closed.Load() {
		t.Fatal("closed borrowed connection pool")
	}
	plain, err := NewMonitor(Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain.Close()
	n := testMonitor(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("proxy password=SECRET") })
	n.Check(context.Background())
	b, _ := json.Marshal(n.Snapshot())
	for _, s := range []string{"SECRET", "networkID", "socks5", "candidates"} {
		if strings.Contains(string(b), s) {
			t.Fatal("leaked implementation", string(b))
		}
	}
}
