package upstreamstate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func playbackDeliveryMonitor(t *testing.T, candidates bool, tr transportFunc, callback func(context.Context, PlaybackURLResponse)) *Monitor {
	t.Helper()
	ch := upstreamrequest.NewChannel()
	if candidates {
		ch.Publish(serialProbeChannels{&candidateFixture{expires: time.Now().Add(time.Hour), second: true, changed: make(chan struct{})}, tr})
	} else {
		ch.Publish(tr)
	}
	m, err := NewMonitor(Options{Channel: ch, OnPlaybackURL: callback})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestPlaybackURLDeliveryReusesProbeResponses(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		name, want, wantRequests := "ordinary", 1, 2
		if candidates {
			name, want, wantRequests = "candidates", 2, 4
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var delivered []PlaybackURLResponse
			var m *Monitor
			m = playbackDeliveryMonitor(t, candidates, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Header.Get("Range") != "" {
					t.Error("playback-only check downloaded a resource")
				}
				resp, err := fixture(r)
				if r.URL.Path == "/Api/Songs/play" {
					resp.Header.Set("Location", strings.Replace(resp.Header.Get("Location"), "/42-", "/43-", 1)+"&token=private-probe-signature")
				}
				return resp, err
			}, func(ctx context.Context, r PlaybackURLResponse) {
				m.Snapshot() // Callback must run outside monitor locks.
				if ctx.Err() != nil {
					t.Error(ctx.Err())
				}
				mu.Lock()
				delivered = append(delivered, r)
				mu.Unlock()
			})
			started := time.Now()
			if err := m.CheckSelected(context.Background(), CheckPlayback); err != nil {
				t.Fatal(err)
			}
			finished := time.Now()
			if len(delivered) != want || int(calls.Load()) != wantRequests {
				t.Fatalf("deliveries=%d, requests=%d; want %d, %d", len(delivered), calls.Load(), want, wantRequests)
			}
			for _, r := range delivered {
				if r.SongID != 42 || r.API != apiBase+"/Api/Songs/play" || r.Node != "cf" || !strings.Contains(r.URL, "/43-") || !strings.HasSuffix(r.URL, "token=private-probe-signature") {
					t.Fatalf("wrong response identity: %+v", r)
				}
				if r.QueryStartedAt.Before(started) || r.ObservedAt.Before(r.QueryStartedAt) || r.ObservedAt.After(finished) {
					t.Fatalf("wrong request timing: %+v", r)
				}
			}
			status, err := json.Marshal(m.Snapshot())
			if err != nil || strings.Contains(string(status), "private-probe-signature") {
				t.Fatalf("private URL leaked into status: %s, %v", status, err)
			}
		})
	}
}

func TestPlaybackURLDeliveryRejectsInvalidCanceledAndRetiredResponses(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		for _, state := range []string{"invalid", "http_error", "retired"} {
			t.Run(state+map[bool]string{false: "/ordinary", true: "/candidates"}[candidates], func(t *testing.T) {
				var m *Monitor
				m = playbackDeliveryMonitor(t, candidates, func(r *http.Request) (*http.Response, error) {
					resp, err := fixture(r)
					if r.URL.Path == "/Api/Songs/play" {
						switch state {
						case "invalid":
							resp.Header.Set("Location", videoFixture+"&s=0")
						case "http_error":
							resp.StatusCode = http.StatusServiceUnavailable
						case "retired":
							// Check retries under the new configuration. Only the
							// retired transport returns a valid URL in this test.
							m.channel.Publish(transportFunc(func(next *http.Request) (*http.Response, error) {
								if next.URL.Path == "/Api/Songs/play" {
									return response(http.StatusServiceUnavailable, ""), nil
								}
								return fixture(next)
							}))
						}
					}
					return resp, err
				}, func(context.Context, PlaybackURLResponse) { t.Error("invalid response delivered") })
				if err := m.CheckSelected(context.Background(), CheckPlayback); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	m := testMonitor(t, fixture)
	m.onPlaybackURL = func(context.Context, PlaybackURLResponse) { t.Error("canceled response delivered") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.deliverPlaybackURL(ctx, m.channel.Snapshot().Revision, PlaybackURLResponse{URL: videoFixture})
}

func TestPlaybackURLDeliveryPrecedesResourceProbe(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		var delivered sync.Map
		var resources atomic.Int32
		m := playbackDeliveryMonitor(t, candidates, func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Range") != "" {
				resources.Add(1)
				if _, ok := delivered.Load(r.URL.String()); !ok {
					t.Error("resource request preceded URL delivery")
				}
				return response(http.StatusServiceUnavailable, ""), nil
			}
			return fixture(r)
		}, func(_ context.Context, r PlaybackURLResponse) { delivered.Store(r.URL, true) })
		if err := m.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if resources.Load() == 0 {
			t.Fatal("no resource probes")
		}
	}
}

func TestPlaybackURLDeliveryCloseCancelsAndWaits(t *testing.T) {
	entered := make(chan struct{}, 2)
	canceled := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	m := playbackDeliveryMonitor(t, false, fixture, func(ctx context.Context, _ PlaybackURLResponse) {
		entered <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		<-release
	})
	t.Cleanup(unblock)
	checkDone := make(chan struct{})
	go func() { defer close(checkDone); _ = m.CheckSelected(context.Background(), CheckPlayback) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel callback")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before callback finished")
	default:
	}
	unblock()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish")
	}
	<-checkDone
}
