package upstreamstate

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type candidateFixture struct {
	mu      sync.Mutex
	expires time.Time
	second  bool
	failed  bool
	changed chan struct{}
	calls   int
}

func (f *candidateFixture) RoundTrip(*http.Request) (*http.Response, error) {
	panic("unselected transport used")
}
func (f *candidateFixture) Changed() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changed
}
func (f *candidateFixture) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return f.Current(target), nil
}
func (f *candidateFixture) Current(target string) []upstreamrequest.Candidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, _ := url.Parse(target)
	var result []upstreamrequest.Candidate
	if !time.Now().Before(f.expires) {
		return result
	}
	for _, id := range []string{"a", "b"} {
		if id == "b" && !f.second {
			continue
		}
		name := id
		result = append(result, upstreamrequest.Candidate{ID: name + "/" + u.Hostname(), Host: u.Hostname(), Mode: "direct", IP: name, ValidUntil: f.expires, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			f.mu.Lock()
			f.calls++
			fail := f.failed && name == "b"
			f.mu.Unlock()
			if fail {
				return response(503, ""), nil
			}
			return fixture(r)
		})})
	}
	return result
}
func candidateMonitor(t *testing.T) (*Monitor, *candidateFixture) {
	t.Helper()
	f := &candidateFixture{expires: time.Now().Add(time.Hour), second: true, changed: make(chan struct{})}
	ch := upstreamrequest.NewChannel()
	ch.Publish(f)
	m, err := newMonitor(Options{}, ch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, f
}

func TestCandidateChecksAreIsolatedAndExecutable(t *testing.T) {
	m, f := candidateMonitor(t)
	f.failed = true
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs := m.Results(Catalog)
	if len(rs) != 6 || rs[0].State != "available" || rs[1].State != "unavailable" {
		t.Fatal(rs)
	}
	selection, ok := m.Recommended(Resource)
	if !ok || !strings.HasPrefix(selection.Result.ChannelID, "a/") {
		t.Fatal(selection, ok)
	}
	r, _ := http.NewRequest("GET", videoFixture, nil)
	if selection.Result.Route == "hkg" {
		r.URL.Host = "nya.xin.moe"
	}
	resp, err := selection.Channel.Transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	f.mu.Lock()
	before := f.calls
	f.mu.Unlock()
	for range 10 {
		m.Results(Resource)
		m.Snapshot()
		m.Recommended(Resource)
	}
	f.mu.Lock()
	after := f.calls
	f.mu.Unlock()
	if before != after {
		t.Fatal("state read started network work")
	}
	observed := rs[0].ObservedAt
	f.mu.Lock()
	f.expires = f.expires.Add(time.Minute)
	f.mu.Unlock()
	if !m.Results(Catalog)[0].ObservedAt.Equal(observed) {
		t.Fatal("unchanged IP refresh lost observation")
	}
	f.mu.Lock()
	f.expires = time.Now().Add(-time.Second)
	f.mu.Unlock()
	if _, ok := m.Recommended(Resource); ok {
		t.Fatal("expired candidate recommended")
	}
}

func TestRecommendationHysteresisAndImmediateFailureFallback(t *testing.T) {
	m, f := candidateMonitor(t)
	sampleAt := time.Now().Add(-time.Minute)
	feed := func(bstate string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		sampleAt = sampleAt.Add(time.Second)
		start := sampleAt
		for _, v := range []struct {
			id, state string
			bytes     int64
		}{{"a", "available", 1000}, {"b", bstate, 3000}} {
			m.record(observation{op: Resource, route: "cf", channel: v.id + "/play.udon.dance", state: v.state, at: sampleAt, latency: time.Millisecond, duration: time.Second, bytes: v.bytes})
		}
		m.updatePreferencesLocked(f, start)
	}
	feed("network_error")
	first, ok := m.Recommended(Resource)
	if !ok || !strings.HasPrefix(first.Result.ChannelID, "a/") {
		t.Fatal(first)
	}
	feed("available")
	for range 20 {
		r, _ := m.Recommended(Resource)
		if r.Result.ChannelID != first.Result.ChannelID {
			t.Fatal("polling bypassed hysteresis")
		}
	}
	feed("available")
	r, _ := m.Recommended(Resource)
	if !strings.HasPrefix(r.Result.ChannelID, "b/") {
		t.Fatal("sustained improvement did not switch", r)
	}
	f.mu.Lock()
	f.second = false
	f.mu.Unlock()
	r, ok = m.Recommended(Resource)
	if !ok || r.Result.ChannelID != first.Result.ChannelID {
		t.Fatal("removed winner did not fall back", r)
	}
}

func TestResourceRecommendationCountsOnlyNewThroughput(t *testing.T) {
	m, f := candidateMonitor(t)
	base := time.Now().Add(-time.Minute)
	feed := func(at time.Time, a, b int64) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, v := range []struct {
			id    string
			bytes int64
		}{{"a", a}, {"b", b}} {
			m.record(observation{op: Resource, route: "cf", channel: v.id + "/play.udon.dance", state: "available", at: at, latency: time.Millisecond, duration: time.Second, transferDuration: time.Second, bytes: v.bytes})
		}
		m.updatePreferencesLocked(f, at)
	}
	feed(base, 3000, 1000)                  // A is initially preferred.
	feed(base.Add(time.Second), 1000, 3000) // B earns one throughput win.
	for i := 2; i < 10; i++ {
		feed(base.Add(time.Duration(i)*time.Second), 0, 0)
		if p := m.preferences[Resource]; p.wins != 1 || !strings.HasSuffix(p.current, "a/play.udon.dance") {
			t.Fatalf("lightweight check changed votes: %+v", p)
		}
	}
	// Even a repeated update with the original batch boundary cannot reuse it.
	m.mu.Lock()
	m.updatePreferencesLocked(f, base)
	m.mu.Unlock()
	if m.preferences[Resource].wins != 1 {
		t.Fatal("same throughput counted twice")
	}
	feed(base.Add(10*time.Second), 1000, 3000)
	if p := m.preferences[Resource]; !strings.HasSuffix(p.current, "b/play.udon.dance") {
		t.Fatalf("second throughput sample did not switch: %+v", p)
	}
}

func TestNewCandidateWakesMonitorWithoutClearingOldSample(t *testing.T) {
	m, f := candidateMonitor(t)
	f.second = false
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool {
		r := m.Results(Catalog)
		return len(r) == 3 && r[0].State == "available" && !m.Snapshot().Checking
	})
	first := m.Results(Catalog)[0].ObservedAt
	f.mu.Lock()
	f.second = true
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
	r := m.Results(Catalog)
	if r[0].ObservedAt.Before(first) {
		t.Fatal("old sample discarded")
	}
	awaitCondition(t, func() bool { r := m.Results(Catalog); return len(r) == 6 && r[1].State == "available" })
}

func TestCandidateConfigChangeKeepsUnaffectedDirectObservations(t *testing.T) {
	m, _ := candidateMonitor(t)
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := m.Results(Catalog)
	replacement := &candidateFixture{expires: time.Now().Add(time.Hour), changed: make(chan struct{})}
	m.channel.Publish(replacement)
	after := m.Results(Catalog)
	if len(after) != 3 || after[0].State != "available" || !after[0].ObservedAt.Equal(before[0].ObservedAt) {
		t.Fatal("unchanged direct path lost history", before, after)
	}
}

type blockedCatalogCandidates struct {
	*candidateFixture
	entered, release chan struct{}
	once             sync.Once
}

func (f *blockedCatalogCandidates) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return f.Current(target), nil
}

func (f *blockedCatalogCandidates) Current(target string) []upstreamrequest.Candidate {
	cs := f.candidateFixture.Current(target)
	for i := range cs {
		transport := cs[i].Transport
		cs[i].Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/Api/Songs/list" {
				f.once.Do(func() {
					close(f.entered)
					select {
					case <-f.release:
					case <-r.Context().Done():
					}
				})
			}
			return transport.RoundTrip(r)
		})
	}
	return cs
}

func TestCandidateChangeDuringCheckTriggersImmediateFollowup(t *testing.T) {
	f := &blockedCatalogCandidates{
		candidateFixture: &candidateFixture{expires: time.Now().Add(time.Hour), changed: make(chan struct{})},
		entered:          make(chan struct{}), release: make(chan struct{}),
	}
	channel := upstreamrequest.NewChannel()
	channel.Publish(f)
	m, err := newMonitor(Options{Policy: Policy{Interval: time.Hour, Lifetime: 2 * time.Hour}}, channel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(f.release) }) }
	t.Cleanup(release)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial check did not enter catalog request")
	}
	// Catalog candidates have already been captured. Multiple DNS updates replace
	// Changed while that check is blocked; the scheduler must retain its subscription.
	f.mu.Lock()
	f.second = true
	for range 2 {
		close(f.changed)
		f.changed = make(chan struct{})
	}
	f.mu.Unlock()
	if r := m.Results(Catalog); len(r) != 6 || r[1].State != "unknown" {
		t.Fatal(r)
	}
	release()
	awaitCondition(t, func() bool {
		r := m.Results(Catalog)
		return len(r) == 6 && r[1].State == "available" && !m.Snapshot().Checking
	})
}
