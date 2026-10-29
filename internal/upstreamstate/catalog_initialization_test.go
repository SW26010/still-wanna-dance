package upstreamstate

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestCatalogInitializationRetriesUntilConsumerAccepts(t *testing.T) {
	for _, failHTTP := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			channel := upstreamrequest.NewChannel()
			var accepted, canAccept atomic.Bool
			var catalogCalls, otherCalls atomic.Int32
			channel.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/Api/Songs/list" || r.URL.Path == "/api/v2/wanna/songs" || r.URL.Path == "/api/wannaInfo" {
					catalogCalls.Add(1)
					if failHTTP && !canAccept.Load() {
						return response(503, ""), nil
					}
				} else {
					otherCalls.Add(1)
					return response(503, ""), nil
				}
				return fixture(r)
			}))
			m, err := NewMonitor(Options{Channel: channel,
				CatalogReady: func(context.Context) bool { return accepted.Load() },
				OnCatalog: func(context.Context, []CatalogResponse) {
					if canAccept.Load() {
						accepted.Store(true)
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			for _, delay := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute} {
				s := m.Snapshot()
				if !s.CatalogInitializing || s.Checking || s.NextCheck.Sub(s.Finished) != delay {
					t.Fatalf("initialization retry lost: delay=%v state=%+v", delay, s)
				}
				catalogBefore, otherBefore := catalogCalls.Load(), otherCalls.Load()
				time.Sleep(delay - time.Nanosecond)
				synctest.Wait()
				if catalogCalls.Load() != catalogBefore {
					t.Fatal("retried before backoff elapsed")
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if catalogCalls.Load() <= catalogBefore || otherCalls.Load() != otherBefore {
					t.Fatal("retry did not limit work to catalog acquisition")
				}
			}
			canAccept.Store(true)
			time.Sleep(time.Minute)
			synctest.Wait()
			s := m.Snapshot()
			if !accepted.Load() || s.CatalogInitializing || s.NextCheck.Sub(s.Finished) <= time.Minute {
				t.Fatalf("accepted catalog did not restore normal monitoring: %+v", s)
			}
			before := catalogCalls.Load()
			time.Sleep(time.Minute)
			synctest.Wait()
			if catalogCalls.Load() != before {
				t.Fatal("initialization retry survived success")
			}
			m.Close()
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			if catalogCalls.Load() != before || m.Snapshot().CatalogInitializing {
				t.Fatal("initialization survived close")
			}
		})
	}
}

func TestCatalogInitializationExistingCatalogAndCancellation(t *testing.T) {
	for _, existing := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			channel := upstreamrequest.NewChannel()
			var calls atomic.Int32
			channel.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if !existing {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return fixture(r)
			}))
			m, err := NewMonitor(Options{Channel: channel, CatalogReady: func(context.Context) bool { return existing }})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.Start()
			synctest.Wait()
			if existing {
				s := m.Snapshot()
				if s.CatalogInitializing || s.NextCheck.Sub(s.Finished) <= time.Minute {
					t.Fatal("existing catalog entered bootstrap retries")
				}
			}
			m.Close()
			before := calls.Load()
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			if calls.Load() != before || m.Snapshot().CatalogInitializing {
				t.Fatal("close did not drain initialization")
			}
		})
	}
}
