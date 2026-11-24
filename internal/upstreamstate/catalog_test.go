package upstreamstate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestColdCatalogKeepsFieldErrorForSync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bad := strings.Replace(catalogFixture, `"id":42`, `"id":42,"volume":"bad"`, 1)
		ch := upstreamrequest.NewChannel()
		ch.Publish(catalogTestPool{
			{ID: "invalid", Mode: "direct", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return response(200, bad), nil
			})},
			{ID: "offline", Mode: "direct", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				time.Sleep(time.Second)
				return response(503, ""), nil
			})},
		})
		m, err := NewMonitor(Options{Channel: ch})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		synced := 0
		upstreamcheck.CheckConnectivity(t.Context(), upstreamcheck.ConnectivityAccess{
			Catalog: func(ctx context.Context, route string) (upstreamcheck.Result, int64, CatalogResponse) {
				if route == "api" {
					return m.ReadCatalog(ctx)
				}
				return upstreamcheck.CatalogFailure(route, upstreamrequest.ErrUnavailable), 0, CatalogResponse{}
			},
		}, upstreamcheck.ConnectivityBusiness{OnCatalog: func(_ context.Context, responses []CatalogResponse) (bool, error) {
			synced++
			if len(responses) != 1 || string(responses[0].Body) != bad {
				t.Fatal("missing invalid catalog body")
			}
			if _, err := responses[0].Parse(); err == nil || !strings.Contains(err.Error(), "volume") {
				t.Fatalf("missing field error: %v", err)
			}
			return false, nil
		}}, upstreamcheck.Catalog, func(upstreamcheck.Result) {})
		if synced != 1 {
			t.Fatalf("got %d sync feedback calls", synced)
		}
	})
}

func TestColdCatalogReaderFollowsSelectedCheck(t *testing.T) {
	for _, tc := range []struct {
		delay time.Duration
		code  int
		state string
	}{{0, 200, "available"}, {0, 503, "upstream_error"}, {0, 0, "channel_unavailable"}, {2 * time.Second, 200, "timeout"}} {
		t.Run(tc.state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				calls := 0
				m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						close(entered)
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					if tc.code == 0 {
						return nil, upstreamrequest.ErrUnavailable
					}
					time.Sleep(tc.delay)
					return response(tc.code, catalogFixture), nil
				})
				read := make(chan string, 1)
				go func() {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					result, id, body := m.ReadCatalog(ctx)
					if result.State == "available" && (id != 42 || string(body.Body) != catalogFixture) {
						t.Error("reader lost the replacement response")
					}
					read <- result.State
				}()
				<-entered
				if err := m.CheckSelected(t.Context(), CheckCatalog); err != nil {
					t.Fatal(err)
				}
				if state := <-read; state != tc.state || calls != 2 {
					t.Fatalf("reader got %s after %d requests", state, calls)
				}
			})
		})
	}
}

func TestColdCatalogReaderUsesPlaybackPrerequisite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		playing := make(chan struct{})
		catalogs := 0
		m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/Api/Songs/list" {
				catalogs++
				return fixture(r)
			}
			close(playing)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		playback := make(chan error, 1)
		go func() { playback <- m.CheckSelected(ctx, CheckPlayback) }()
		<-playing
		reader, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		result, id, body := m.ReadCatalog(reader)
		stop()
		<-playback
		if result.State != "available" || id != 42 || string(body.Body) != catalogFixture || catalogs != 1 {
			t.Fatalf("prerequisite returned %s, song %d, %d requests", result.State, id, catalogs)
		}
		if _, ok := m.Recommended(Catalog); ok {
			t.Fatal("playback check changed catalog recommendations")
		}
	})
}

func TestCatalogTimingSpansDifferentSampleSongs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		song := 0
		m := testMonitor(t, func(*http.Request) (*http.Response, error) {
			time.Sleep(time.Duration(song) * time.Second)
			return response(200, strings.Replace(catalogFixture, `"id":42`, fmt.Sprintf(`"id":%d`, song), 1)), nil
		})
		for _, id := range []int{1, 3} {
			song = id
			if err := m.CheckSelected(t.Context(), CheckCatalog); err != nil {
				t.Fatal(err)
			}
		}
		r := resultFor(t, m, Catalog, "api")
		if r.Samples != 2 || r.ProbeDurationMS == nil || *r.ProbeDurationMS != 2000 {
			t.Fatal(r)
		}
	})
}

func TestColdCatalogReadJoinsScheduledProbeAndRejectsRetiredChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		<-entered
		finished := make(chan error, 1)
		go func() { result, _, _ := m.ReadCatalog(t.Context()); finished <- result.Err }()
		synctest.Wait()
		m.channel.Release(m.channel.Snapshot().Revision)
		if err := <-finished; !errors.Is(err, upstreamrequest.ErrRetired) {
			t.Fatalf("retired catalog read: %v", err)
		}
		m.Close()
	})
}

func TestCatalogDeliveryReusesAllProbeResponses(t *testing.T) {
	for _, mode := range []string{"ordinary", "candidates"} {
		t.Run(mode, func(t *testing.T) {
			var m *Monitor
			var requests int
			var f *candidateFixture
			want, wantRequests := 1, 1
			if mode == "candidates" {
				m, f = candidateMonitor(t)
				want, wantRequests = 2, 2
			} else {
				m = testMonitor(t, func(r *http.Request) (*http.Response, error) { requests++; return fixture(r) })
			}
			calls := 0
			m.onCatalog = func(ctx context.Context, responses []CatalogResponse) {
				calls++
				m.Snapshot() // Callback cannot hold the monitor lock.
				if ctx.Err() != nil || len(responses) != want {
					t.Errorf("responses=%d, error=%v", len(responses), ctx.Err())
				}
				for _, r := range responses {
					if r.Source != entry(Catalog, r.Route) || len(r.Body) == 0 {
						t.Errorf("bad response: %+v", r)
					}
				}
			}
			if err := m.CheckSelected(context.Background(), CheckCatalog); err != nil {
				t.Fatal(err)
			}
			if f != nil {
				f.mu.Lock()
				requests = f.calls
				f.mu.Unlock()
			}
			if calls != 1 || requests != wantRequests {
				t.Fatalf("deliveries=%d, requests=%d, want %d", calls, requests, wantRequests)
			}
		})
	}
}

func TestCatalogDeliveryRejectsCanceledAndRetiredBatches(t *testing.T) {
	m := testMonitor(t, fixture)
	m.onCatalog = func(context.Context, []CatalogResponse) { t.Error("invalid batch delivered") }
	r := []CatalogResponse{{Body: []byte(catalogFixture)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.deliverCatalogs(ctx, m.channel.Snapshot().Revision, r)
	m.deliverCatalogs(context.Background(), m.channel.Snapshot().Revision+1, r)
}

func TestInvalidProbeDoesNotDeliverCatalog(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { return response(200, `{}`), nil })
	m.onCatalog = func(context.Context, []CatalogResponse) { t.Error("invalid response delivered") }
	if err := m.CheckSelected(context.Background(), CheckCatalog); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRecommendationRequiresFullValidation(t *testing.T) {
	body := strings.Replace(catalogFixture, `"id":42`, `"id":42,"volume":"bad"`, 1)
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
	if err := m.CheckSelected(t.Context(), CheckCatalog); err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, m, Catalog, "api")
	if result.HTTP != 200 || result.State != "unavailable" || result.Reason != "invalid" {
		t.Fatalf("HTTP 200 with invalid metadata was not rejected: %+v", result)
	}
	if _, ok := m.Recommended(Catalog); ok {
		t.Fatal("invalid catalog was recommended")
	}
}

func TestCatalogRecommendationRanksCompleteReadTime(t *testing.T) {
	m, f := candidateMonitor(t)
	at := time.Now().Add(-time.Minute)
	feed := func(slowerStartDuration time.Duration) {
		m.mu.Lock()
		defer m.mu.Unlock()
		at = at.Add(time.Second)
		for _, sample := range []struct {
			channel           string
			latency, duration time.Duration
		}{
			{"a/api.udon.dance", time.Millisecond, time.Second},
			{"b/api.udon.dance", 10 * time.Millisecond, slowerStartDuration},
		} {
			recordFixture(m, observation{op: Catalog, route: "api", channel: sample.channel, state: "available", at: at, latency: sample.latency, duration: sample.duration})
		}
		m.updatePreferencesLocked(f, at, CheckCatalog)
	}
	for _, round := range []struct {
		slowerStartDuration time.Duration
		want                string
	}{
		{1200 * time.Millisecond, "a"},
		{100 * time.Millisecond, "a"},
		{100 * time.Millisecond, "b"},
	} {
		feed(round.slowerStartDuration)
		s, ok := m.Recommended(Catalog)
		if !ok || s.Channel.IP != round.want {
			t.Fatalf("catalog preference IP=%q, want %q", s.Channel.IP, round.want)
		}
	}
}

func TestPlaybackUdonPrerequisiteDeliveredOnce(t *testing.T) {
	requests := 0
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/list" {
			requests++
		}
		return fixture(r)
	})
	deliveries := 0
	m.onCatalog = func(_ context.Context, r []CatalogResponse) {
		deliveries++
		if len(r) != 1 || r[0].Route != "api" {
			t.Fatal(r)
		}
	}
	for i := 0; i < 2; i++ {
		if err := m.CheckSelected(context.Background(), CheckPlayback); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 || deliveries != 1 {
		t.Fatal("monitor did not fetch/reuse Udon sample", requests)
	}
}
