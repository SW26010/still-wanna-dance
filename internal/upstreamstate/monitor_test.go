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
	resp := response(206, strings.Repeat("x", 131072))
	resp.Header.Set("Content-Range", "bytes 0-131071/131072")
	return resp, nil
}
func resultFor(t *testing.T, m *Monitor, op Operation, route string) Result {
	t.Helper()
	for _, r := range m.Results(op) {
		if r.Route == route {
			return r
		}
	}
	t.Fatalf("missing result for %s/%s", op, route)
	return Result{}
}

func TestAllRoutesAndReadOnlySnapshot(t *testing.T) {
	var calls atomic.Int32
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Range") != "" && (r.Header.Get("Range") != "bytes=0-131071" || r.URL.Scheme != "https") {
			t.Error(r)
		}
		if r.Header.Get("Range") != "" {
			time.Sleep(time.Millisecond)
		}
		return fixture(r)
	})
	initial := m.Snapshot()
	if len(initial.Results) != 5 {
		t.Fatal(initial)
	}
	for _, r := range initial.Results {
		if r.State != "unknown" || r.Route == "" || r.Entry == "" || r.EstimatedLatencyMS != nil {
			t.Fatal(r)
		}
	}
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot()
	if len(snapshot.Results) != 5 {
		t.Fatal(snapshot)
	}
	for _, r := range snapshot.Results {
		if r.State != "available" || r.EstimatedLatencyMS == nil {
			t.Fatal(r)
		}
		if r.Operation == PlaybackURL && (r.EstimatedSpeedBPS != nil || r.SampleSongID != 42) {
			t.Fatal(r)
		}
		if r.Operation == Resource && (r.EstimatedSpeedBPS == nil || r.SampleSongID != 42) {
			t.Fatal(r)
		}
	}
	if resultFor(t, m, Catalog, "api").Entry != apiBase+"/Api/Songs/list" {
		t.Fatal(snapshot)
	}
	for _, op := range []Operation{PlaybackURL, Resource} {
		results := m.Results(op)
		if len(results) != 2 || results[0].Route != "cf" || results[1].Route != "hkg" {
			t.Fatal(results)
		}
	}
	for range 50 {
		m.Snapshot()
		m.Results(Resource)
	}
	if calls.Load() != 5 {
		t.Fatal("read triggered I/O", calls.Load())
	}
	snapshot.Results[0].Entry = "changed"
	*snapshot.Results[3].EstimatedSpeedBPS = -1
	resources := m.Results(Resource)
	resources[0].Route = "changed"
	*resources[1].EstimatedLatencyMS = -1
	if resultFor(t, m, Catalog, "api").Entry == "changed" || *resultFor(t, m, Resource, "cf").EstimatedSpeedBPS < 0 || *resultFor(t, m, Resource, "hkg").EstimatedLatencyMS < 0 {
		t.Fatal("snapshot aliases state")
	}
	if len(m.Results(Operation("invalid"))) != 0 {
		t.Fatal("invented an unsupported operation")
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
	if got := resultFor(t, m, Catalog, "api"); got.State != "unavailable" || got.Reason != "origin_timeout" || got.Entry == "" {
		t.Fatal(got)
	}
	if resultFor(t, m, PlaybackURL, "cf").State != "unknown" || plays.Load() != 0 {
		t.Fatal("guessed sample")
	}
	failed.Store(false)
	m.Check(context.Background())
	failed.Store(true)
	m.Check(context.Background())
	if resultFor(t, m, Catalog, "api").State != "unavailable" || resultFor(t, m, PlaybackURL, "cf").State != "available" || plays.Load() != 4 {
		t.Fatal("catalog failure poisoned other operations")
	}
	p := DefaultPolicy()
	p.SampleLifetime = time.Nanosecond
	p.Lifetime = time.Nanosecond
	m.SetPolicy(p)
	time.Sleep(time.Millisecond)
	m.Check(context.Background())
	if plays.Load() != 4 || resultFor(t, m, Resource, "cf").State != "stale" {
		t.Fatal("used expired sample")
	}
}

func TestPartialCheckKeepsBothRoutesAndTheirFailures(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/play" && r.URL.Query().Get("node") == "nya" {
			select {
			case <-release:
				return response(524, ""), nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return fixture(r)
	})
	done := make(chan error, 1)
	go func() { done <- m.Check(context.Background()) }()
	awaitCondition(t, func() bool { return resultFor(t, m, Resource, "cf").State == "available" })
	for _, op := range []Operation{PlaybackURL, Resource} {
		results := m.Results(op)
		if len(results) != 2 || results[0].Route != "cf" || results[0].State != "available" || results[1].Route != "hkg" || results[1].State != "unknown" {
			t.Fatal("partial check hid an unfinished route", results)
		}
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("check did not finish")
	}
	for _, op := range []Operation{PlaybackURL, Resource} {
		results := m.Results(op)
		if results[0].State != "available" || results[1].State != "unavailable" || results[1].HTTP != 524 || results[1].Stage != "headers" {
			t.Fatal("lost per-route diagnostics", results)
		}
	}
	if resultFor(t, m, PlaybackURL, "hkg").Reason != "origin_timeout" || resultFor(t, m, Resource, "hkg").Reason != "resolution_unavailable" {
		t.Fatal("lost operation-specific failure reason", m.Snapshot())
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
		for _, r := range m.Results(op) {
			if r.State != "unavailable" || r.Entry == "" {
				t.Fatal(r)
			}
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
	if s := m.Snapshot(); s.Checking || !s.Closed || resultFor(t, m, Catalog, "api").State != "closed" {
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
	if r := resultFor(t, n, Catalog, "api"); r.State != "unavailable" || r.Reason != "timeout" {
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
