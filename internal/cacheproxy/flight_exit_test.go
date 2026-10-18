package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type flightEventWriter func([]byte) (int, error)

func (f flightEventWriter) Write(p []byte) (int, error) { return f(p) }

func TestGETArrivingAfterIdleSnapshotKeepsFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := demandLifecycleSession(t)
		_, release := s.addDemand(context.Background(), w.v, false)
		reconcileSession(t, w)
		release() // Batch withdrawal has no handoff, so this plan is idle.
		now := time.Now()
		plan, changed, active := w.plan(now, false)
		if len(plan) != 0 {
			t.Fatal("test did not capture an idle plan")
		}
		_, releaseGET := s.addDemand(context.Background(), w.v, true)
		defer releaseGET()
		if _, retry, err := w.reconcile(plan, now, changed, active); err != nil || !retry {
			t.Fatal("concurrent GET did not invalidate idle snapshot", retry, err)
		}
		if w.f.preempted || w.jobs[0].retiring {
			t.Fatal("stale plan committed flight/task retirement")
		}
		plan, changed, active = w.plan(now, false)
		if _, retry, err := w.reconcile(plan, now, changed, active); err != nil || retry {
			t.Fatal("fresh GET plan was not applied", retry, err)
		}
		if len(w.jobs[0].needs) != 1 || w.f.preempted {
			t.Fatal("GET could not continue the existing flight")
		}
	})
}

func TestNewGETWaitsForExitingFlightCleanup(t *testing.T) {
	for _, status := range []int{http.StatusPartialContent, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, cfg := setup(t, nil)
			events := make(chan string, 8)
			s.cfg.Logger = slog.New(slog.NewTextHandler(flightEventWriter(func(p []byte) (int, error) {
				for _, event := range []string{"cache_capacity_wait", "cache_task_attached"} {
					if strings.Contains(string(p), event) {
						events <- event
					}
				}
				return len(p), nil
			}), nil))
			cleanup, gate := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(gate) })
			defer unblock()
			var loads, requests atomic.Int32
			s.cfg.BeginResourceLoad = func() func() {
				if loads.Add(1) == 1 {
					return func() { close(cleanup); <-gate }
				}
				return func() {}
			}
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
					if requests.Add(1) == 1 {
						body = stalledResourceBody{r.Context()}
					}
					h := make(http.Header)
					if status == http.StatusPartialContent {
						h.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
					}
					return &http.Response{StatusCode: status, Header: h, ContentLength: int64(len(payload)), Body: body, Request: r}, nil
				})}
			}
			r, _ := http.NewRequest("GET", videoURL(payload), nil)
			v, err := s.parseResolved(r)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			firstCtx, cancelFirst := context.WithCancel(ctx)
			defer cancelFirst()
			old, reader, err := s.obtain(firstCtx, v)
			if err != nil || reader == nil {
				t.Fatal("first GET did not stream", err)
			}
			<-events // The first attachment precedes the handoff/cleanup gap.
			reader.Close()
			cancelFirst()
			receiveDemandEvent(t, cleanup)
			type outcome struct {
				flight *flight
				body   []byte
				err    error
			}
			result := make(chan outcome, 1)
			go func() {
				f, reader, err := s.obtain(ctx, v)
				var body []byte
				if err == nil {
					if reader != nil {
						reader.verifyFull = true
						body, err = io.ReadAll(reader)
						reader.Close()
					} else {
						body, err = os.ReadFile(f.path)
					}
				}
				result <- outcome{f, body, err}
			}()
			if event := receiveDemandEvent(t, events); event != "cache_capacity_wait" {
				t.Error("new GET attached before old cleanup finished", event)
			}
			if requests.Load() != 1 {
				t.Error("replacement started before old cleanup finished")
			}
			unblock()
			got := receiveDemandEvent(t, result)
			if got.err != nil || got.flight == old || string(got.body) != payload {
				t.Fatalf("GET did not recover after cleanup: old=%v bytes=%d error=%v", got.flight == old, len(got.body), got.err)
			}
			if body, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(body) != payload {
				t.Fatal("replacement was not verified and published", err)
			}
		})
	}
}
