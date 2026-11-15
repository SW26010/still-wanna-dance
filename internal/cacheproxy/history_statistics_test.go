package cacheproxy

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestHistoryStatisticsLimitsRawRequestsBeforeValidSamples(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.UnixMilli(300000000)
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10005)
 INSERT INTO request_events(requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes)
 SELECT ?,CASE WHEN i>10000 THEN 'HEAD' ELSE 'GET' END,'','canceled',1,1,206,i*1000000,0,2,3,5 FROM n`, now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	v, err := s.History().Statistics(t.Context(), now, false)
	if err != nil || v.Requests != 10000 || v.Limit != 10000 || v.SourceSamples != 9995 || v.Ready.Samples != 9995 || v.Blocking.Samples != 0 || v.Ready.P50MS == nil || *v.Ready.P50MS != 5003 || *v.Ready.P95MS != 9501 || v.Sources != (ResponseSources{DownloadBytes: 19990, CacheBytes: 29985, LocalBytes: 49975}) {
		t.Fatalf("raw sample/nearest rank: %+v %v", v, err)
	}
	if v.From != now.Add(-72*time.Hour).UnixMilli() || v.To != now.UnixMilli() || v.OldestAt == nil || *v.OldestAt != now.Add(-time.Hour).UnixMilli() || s.History().current != nil {
		t.Fatalf("coverage/unexpected copy: %+v", v)
	}
}

func TestHistoryStatisticsCacheTTLManualRefreshAndFailure(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	now := time.UnixMilli(300000000)
	ready := false
	e := usageEvent{method: "GET", at: now.Add(-time.Minute).UnixMilli(), status: 200, bytes: 1, firstBodyNS: 123, firstBodyBlocking: &ready, sources: &ResponseSources{DownloadBytes: 7}}
	if err := s.usage.write([]usageEvent{e}); err != nil {
		t.Fatal(err)
	}
	h := s.History()
	first, err := h.Statistics(t.Context(), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.usage.write([]usageEvent{e}); err != nil {
		t.Fatal(err)
	}
	got, err := h.Statistics(t.Context(), now.Add(59*time.Second), false)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("cached data/time: %+v %v", got, err)
	}
	*got.Ready.P50MS = -1
	*got.OldestAt = -1
	got, err = h.Statistics(t.Context(), now.Add(time.Minute), false)
	if err != nil || got.Requests != 2 || got.Sources.DownloadBytes != 14 || *got.Ready.P50MS != 0.000123 {
		t.Fatalf("expiry/mutation: %+v %v", got, err)
	}
	if err := s.usage.write([]usageEvent{e}); err != nil {
		t.Fatal(err)
	}
	got, err = h.Statistics(t.Context(), now.Add(time.Minute+time.Second), true)
	if err != nil || got.Requests != 3 {
		t.Fatalf("manual refresh: %+v %v", got, err)
	}
	original := h.path
	h.path = storagePath(t.TempDir())
	if _, err = h.Statistics(t.Context(), now.Add(62*time.Second), true); !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", err)
	}
	h.path = original
	if got, err = h.Statistics(t.Context(), now.Add(62*time.Second), true); err != nil || got.Requests != 3 {
		t.Fatalf("failed refresh cached: %+v %v", got, err)
	}
	h.Close()
	if _, err = h.Statistics(t.Context(), now, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed: %v", err)
	}
}

func TestHistoryStatisticsConcurrentRefreshAndCancellation(t *testing.T) {
	for _, outcome := range []string{"success", "cancel-leader", "cancel-waiter", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache historyCache[int]
				if _, err := cache.get(t.Context(), false, func(int) bool { return true }, func() (int, error) { return 1, nil }); err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int64
				failure := errors.New("controlled statistics failure")
				read := func() (int, error) {
					if calls.Add(1) == 1 {
						close(entered)
						<-release
					}
					if outcome == "failure" {
						return 0, failure
					}
					return 2, nil
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				waitCtx, cancelWait := context.WithCancel(t.Context())
				defer cancelWait()
				leader, waiters := make(chan error, 1), make(chan error, 8)
				go func() { _, err := cache.get(ctx, true, func(int) bool { return true }, read); leader <- err }()
				<-entered
				for i := 0; i < 8; i++ {
					go func() {
						v, err := cache.get(waitCtx, true, func(int) bool { return true }, read)
						if err == nil && v != 2 {
							err = errors.New("stale manual result")
						}
						waiters <- err
					}()
				}
				synctest.Wait()
				if outcome == "cancel-leader" {
					cancel()
				}
				if outcome == "cancel-waiter" {
					cancelWait()
				}
				close(release)
				err := <-leader
				if outcome == "cancel-leader" {
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				} else if outcome == "failure" {
					if !errors.Is(err, failure) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 8; i++ {
					err := <-waiters
					if outcome == "cancel-waiter" {
						if !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
					} else if outcome == "failure" {
						if !errors.Is(err, failure) {
							t.Fatal(err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
				}
				want := int64(1)
				if outcome == "cancel-leader" {
					want = 2
				}
				if calls.Load() != want {
					t.Fatalf("computations %d want %d", calls.Load(), want)
				}
			})
		})
	}
}
