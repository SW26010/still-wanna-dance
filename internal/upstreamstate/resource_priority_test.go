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

type priorityBody struct {
	ctx    context.Context
	closed chan struct{}
	once   sync.Once
}

func (b *priorityBody) Read([]byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
}
func (b *priorityBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestAutomaticResourcePreemptionAndResume(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		{
			name := map[bool]string{false: "ordinary", true: "candidates"}[candidates]
			t.Run(name, func(t *testing.T) {
				entered, closed := make(chan struct{}), make(chan struct{})
				var resources atomic.Int32
				tr := transportFunc(func(r *http.Request) (*http.Response, error) {
					resp, err := fixture(r)
					if r.Header.Get("Range") != "" && resources.Add(1) == 1 {
						resp.Body.Close()
						resp.Body = &priorityBody{ctx: r.Context(), closed: closed}
						close(entered)
					}
					return resp, err
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
				if err := m.Start(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("no automatic resource")
				}
				end1, end2 := ch.BeginResourceLoad(), ch.BeginResourceLoad()
				defer end1()
				defer end2()
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("active probe not canceled")
				}
				if !m.Snapshot().ResourcesPaused {
					t.Fatal("missing paused status")
				}
				end1()
				if !m.Snapshot().ResourcesPaused {
					t.Fatal("resumed with a second business load active")
				}
				for _, r := range m.Results(Resource) {
					if r.State == "unavailable" {
						t.Fatal("preemption poisoned result", r)
					}
				}
				end2()
				awaitCondition(t, func() bool { return !m.Snapshot().Finished.IsZero() })
				for _, r := range m.Results(Resource) {
					if r.State != "available" || r.Samples != 1 {
						t.Fatalf("bad resumed sample: %+v", r)
					}
				}
			})
		}
	}
}

func TestManualResourceCheckBypassesBusyBusiness(t *testing.T) {
	m := testMonitor(t, fixture)
	end := m.channel.BeginResourceLoad()
	defer end()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Results(Resource) {
		if r.State != "available" {
			t.Fatal(r)
		}
	}
}

func TestCloseCancelsAutomaticProbeWaitingForBusiness(t *testing.T) {
	m := testMonitor(t, fixture)
	end := m.channel.BeginResourceLoad()
	defer end()
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return resultFor(t, m, Catalog, "api").State == "available" })
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close blocked on busy business")
	}
}
