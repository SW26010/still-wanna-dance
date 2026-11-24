package upstreamstate

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type catalogTestPool []upstreamrequest.Candidate

func (p catalogTestPool) Changed() <-chan struct{} { return nil }
func (p catalogTestPool) Current(target string) []upstreamrequest.Candidate {
	if target == entry(Catalog, "api") {
		return p
	}
	return nil
}
func (p catalogTestPool) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return p.Current(target), nil
}

func sampledMonitor(t *testing.T, request func(int, *http.Request) (*http.Response, error)) *Monitor {
	t.Helper()
	var pool catalogTestPool
	for i := range 100 {
		pool = append(pool, upstreamrequest.Candidate{ID: strconv.Itoa(i), Mode: "direct", Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			return request(i, r)
		})})
	}
	channel := upstreamrequest.NewChannel()
	channel.Publish(pool)
	m, err := NewMonitor(Options{Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestCatalogSamplingPublishesEarlyAndRenewsWinner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		seen := make(map[int]bool)
		running, peak := 0, 0
		m := sampledMonitor(t, func(i int, r *http.Request) (*http.Response, error) {
			mu.Lock()
			if seen[i] {
				t.Errorf("repeated candidate %d in one round", i)
			}
			seen[i] = true
			running++
			peak = max(peak, running)
			mu.Unlock()
			time.Sleep(2 * time.Second)
			mu.Lock()
			running--
			mu.Unlock()
			return fixture(r)
		})
		for round := range 2 {
			previous, warm := m.Recommended(Catalog)
			started := time.Now()
			done := make(chan error, 1)
			go func() { done <- m.CheckSelected(t.Context(), CheckCatalog) }()
			synctest.Wait()
			if warm {
				id, _ := strconv.Atoi(previous.Channel.ID)
				mu.Lock()
				startedWinner := seen[id]
				mu.Unlock()
				if !startedWinner {
					t.Fatalf("winner %d missing from first wave", id)
				}
			}
			time.Sleep(2 * time.Second)
			synctest.Wait()
			if _, ok := m.Recommended(Catalog); !ok {
				t.Fatal("no recommendation after first complete reads")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if len(seen) < catalogSuccesses || len(seen) > catalogSuccesses+catalogParallel-1 || peak != catalogParallel || running != 0 {
				t.Fatalf("round %d probed %d, peak %d, running %d", round, len(seen), peak, running)
			}
			if time.Since(started) > 4*time.Second {
				t.Fatal("healthy sample did not converge in two waves")
			}
			clear(seen)
		}
	})
}

func TestCatalogSamplingBoundsUnusablePool(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(strconv.FormatBool(stalled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := sampledMonitor(t, func(_ int, r *http.Request) (*http.Response, error) {
					if stalled {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return response(503, ""), nil
				})
				start := time.Now()
				if err := m.CheckSelected(t.Context(), CheckCatalog); err != nil {
					t.Fatal(err)
				}
				failures := 0
				for _, r := range m.Results(Catalog) {
					switch r.State {
					case "unavailable", "stale": // Early timeouts can reach FailureLifetime during the round.
						failures++
					case "unknown":
					default:
						t.Fatalf("unexpected state %q", r.State)
					}
				}
				if failures == 0 || failures > catalogAttempts || (!stalled && failures != catalogAttempts) {
					t.Fatalf("unexpected number of failed candidates %d", failures)
				}
				if elapsed := time.Since(start); elapsed > 37500*time.Millisecond {
					t.Fatalf("stalled round took %s", elapsed)
				}
			})
		})
	}
}

func TestCatalogSamplingFindsSlowFallbackWhenWinnerFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := sampledMonitor(t, func(i int, r *http.Request) (*http.Response, error) {
			if i == 0 {
				return response(http.StatusServiceUnavailable, ""), nil
			}
			select {
			case <-time.After(9 * time.Second):
				return fixture(r)
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		})
		m.mu.Lock()
		m.record(observation{op: Catalog, route: "api", channel: "0", state: "available", duration: time.Second, at: time.Now()})
		m.preferences = map[Operation]*preference{Catalog: {current: "api/0"}}
		m.mu.Unlock()
		if err := m.CheckSelected(t.Context(), CheckCatalog); err != nil {
			t.Fatal(err)
		}
		if s, ok := m.Recommended(Catalog); !ok || s.Channel.ID == "0" {
			t.Fatalf("no measured fallback after winner failed: %+v", s)
		}
	})
}

func TestCatalogSamplingPreservesObservationsUntilFullTimeout(t *testing.T) {
	for _, tc := range []struct {
		name         string
		priorRead    time.Duration
		fullTimeouts int
	}{
		{"round deadline", 16 * time.Second, catalogParallel},
		{"exploration deadline", time.Second, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := atomic.Int32{}
				m := sampledMonitor(t, func(_ int, r *http.Request) (*http.Response, error) {
					attempts.Add(1)
					<-r.Context().Done()
					return nil, r.Context().Err()
				})
				previous := time.Now().Add(-time.Minute)
				m.mu.Lock()
				for i := range 100 {
					m.record(observation{op: Catalog, route: "api", channel: strconv.Itoa(i), state: "available", duration: tc.priorRead, at: previous})
				}
				m.mu.Unlock()
				if err := m.CheckSelected(t.Context(), CheckCatalog); err != nil {
					t.Fatal(err)
				}
				failures := 0
				for _, r := range m.Results(Catalog) {
					if r.State == "unavailable" && r.Reason == "timeout" {
						failures++
					} else if r.State != "available" || r.ObservedAt != previous || r.ValidUntil != previous.Add(m.policy.Lifetime) {
						t.Fatalf("unfinished observation changed: %+v", r)
					}
				}
				if failures != tc.fullTimeouts || int(attempts.Load()) <= failures {
					t.Fatalf("got %d timeouts from %d attempts, want %d full timeouts", failures, attempts.Load(), tc.fullTimeouts)
				}
			})
		})
	}
}

func TestCatalogSamplingSchedulesRenewalForWinner(t *testing.T) {
	m := sampledMonitor(t, nil)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, at := range map[string]time.Time{"0": now, "1": now.Add(-m.policy.Lifetime + time.Second)} {
		m.record(observation{op: Catalog, route: "api", channel: id, state: "available", duration: time.Second, at: at})
	}
	m.preferences = map[Operation]*preference{Catalog: {current: "api/0"}}
	m.nextCheck = now.Add(m.policy.Interval)
	m.scheduleCandidatesLocked(m.channel.Snapshot().Candidates)
	if m.nextCheck.Before(now.Add(4 * time.Minute)) {
		t.Fatalf("unselected alternative triggered premature renewal at %v", m.nextCheck)
	}
}

func TestPlaybackCheckRenewsCurrentCatalogRecommendation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var probes atomic.Int32
		m := sampledMonitor(t, func(_ int, r *http.Request) (*http.Response, error) {
			probes.Add(1)
			return fixture(r)
		})
		now := time.Now()
		// The DNS winner has left the pool since the last catalog check.
		// Two presets remain healthy; path 1 is faster.
		m.preferences = map[Operation]*preference{Catalog: {current: "api/expired-dns"}}
		m.record(observation{op: Catalog, route: "api", channel: "1", state: "available", duration: time.Second, at: now.Add(-4 * time.Minute)})
		m.record(observation{op: Catalog, route: "api", channel: "2", state: "available", duration: 2 * time.Second, at: now.Add(-5 * time.Minute)})
		m.songID, m.songAt = 42, now
		m.nextCheck = now.Add(m.policy.Interval)
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // The automatic scheduler is waiting on its previous timer.
		if err := m.CheckSelected(t.Context(), CheckPlayback); err != nil {
			t.Fatal(err)
		}
		winner, ok := m.Recommended(Catalog)
		if !ok || winner.Channel.ID != "1" {
			t.Fatalf("wrong recommendation: %+v", winner)
		}
		margin := min(m.policy.Lifetime/3, 2*m.policy.RequestTimeout+m.policy.ResourceTimeout)
		due := winner.Result.ObservedAt.Add(m.policy.Lifetime - margin)
		if got := m.Snapshot().NextCheck; !got.Equal(due) {
			t.Fatalf("scheduled %v, want %v", got, due)
		}
		time.Sleep(time.Until(due))
		synctest.Wait()
		if probes.Load() == 0 {
			t.Fatal("scheduler slept past the current recommendation's renewal")
		}
	})
}
