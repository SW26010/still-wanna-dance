package upstreamstate

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type serialProbeChannels struct {
	*candidateFixture
	transport http.RoundTripper
}

func TestPlaybackRoutesProgressIndependentlyWithSingleAndMultipleCandidates(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		t.Run(map[bool]string{false: "single candidate", true: "multiple candidates"}[candidates], func(t *testing.T) {
			cfStarted, otherResource := make(chan struct{}), make(chan struct{})
			gate := make(chan struct{})
			var cfOnce, otherOnce sync.Once
			tr := transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/Api/Songs/play" && r.URL.Query().Get("node") == "cf" {
					cfOnce.Do(func() { close(cfStarted) })
					select {
					case <-gate:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				if r.Header.Get("Range") != "" && r.URL.Host == "nya.xin.moe" {
					otherOnce.Do(func() { close(otherResource) })
				}
				return fixture(r)
			})
			ch := upstreamrequest.NewChannel()
			if candidates {
				ch.Publish(serialProbeChannels{&candidateFixture{expires: time.Now().Add(time.Hour), second: true, changed: make(chan struct{})}, tr})
			} else {
				ch.Publish(tr)
			}
			m, err := newMonitor(Options{}, ch)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- m.CheckSelected(ctx, CheckConnectivity) }()
			select {
			case <-cfStarted:
			case <-time.After(time.Second):
				t.Fatal("CF did not start")
			}
			select {
			case <-otherResource:
			case <-time.After(time.Second):
				t.Fatal("CF stalled the other route's resource probe")
			}
			close(gate)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (f serialProbeChannels) Current(target string) []upstreamrequest.Candidate {
	cs := f.candidateFixture.Current(target)
	for i := range cs {
		cs[i].Transport = f.transport
	}
	return cs
}
func (f serialProbeChannels) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return f.Current(target), nil
}

type measuredBody struct {
	io.ReadCloser
	active *atomic.Int32
}

func (b measuredBody) Read(p []byte) (int, error) {
	// Keep the transfer active after headers, including body consumption.
	time.Sleep(5 * time.Millisecond)
	return b.ReadCloser.Read(p)
}
func (b measuredBody) Close() error {
	b.active.Add(-1)
	return b.ReadCloser.Close()
}

func TestResourceProbesNeverOverlap(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		t.Run(map[bool]string{false: "single candidate", true: "multiple candidates"}[candidates], func(t *testing.T) {
			var active, count atomic.Int32
			tr := transportFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := fixture(r)
				if r.Header.Get("Range") != "" {
					if active.Add(1) != 1 {
						t.Error("resource throughput probes overlap")
					}
					count.Add(1)
					resp.Body = measuredBody{resp.Body, &active}
				}
				return resp, err
			})
			ch := upstreamrequest.NewChannel()
			want := int32(2)
			if candidates {
				ch.Publish(serialProbeChannels{&candidateFixture{expires: time.Now().Add(time.Hour), second: true, changed: make(chan struct{})}, tr})
				want = 4
			} else {
				ch.Publish(tr)
			}
			m, err := newMonitor(Options{}, ch)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if count.Load() != want || active.Load() != 0 {
				t.Fatalf("missing or unfinished probes: count %d, active %d", count.Load(), active.Load())
			}
			for _, r := range m.Results(Resource) {
				if r.State != "available" || r.EstimatedSpeedBPS == nil {
					t.Fatalf("missing throughput: %+v", r)
				}
			}
		})
	}
}
