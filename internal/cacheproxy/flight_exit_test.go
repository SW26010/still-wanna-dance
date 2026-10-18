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

type flightEventHandler struct {
	slog.Handler
	handle func(slog.Record)
}

func (h flightEventHandler) Handle(ctx context.Context, r slog.Record) error {
	// Run outside the text handler's lock so a delayed coordinator does not
	// block logs from the executor or an arriving GET.
	h.handle(r)
	return h.Handler.Handle(ctx, r)
}
func (h flightEventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}
func (h flightEventHandler) WithGroup(name string) slog.Handler {
	h.Handler = h.Handler.WithGroup(name)
	return h
}

func TestGETArrivingAfterIdleSnapshotKeepsFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := demandLifecycleSession(t)
		_, release := addTestDemand(t, s, context.Background(), w.v, false)
		reconcileSession(t, w)
		release() // Batch withdrawal has no handoff, so this plan is idle.
		now := time.Now()
		plan, changed, active := w.plan(now, false)
		if len(plan) != 0 {
			t.Fatal("test did not capture an idle plan")
		}
		_, releaseGET := addTestDemand(t, s, context.Background(), w.v, true)
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
	testNewGETDuringCleanup(t, false)
}

func TestNewGETDuringBlockPause(t *testing.T) {
	testNewGETDuringCleanup(t, true)
}

func testNewGETDuringCleanup(t *testing.T, pauseBlock bool) {
	t.Helper()
	for _, status := range []int{http.StatusPartialContent, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, cfg := setup(t, nil)
			events := make(chan string, 8)
			coordinatorGate := make(chan struct{})
			resumeCoordinator := sync.OnceFunc(func() { close(coordinatorGate) })
			defer resumeCoordinator()
			s.cfg.Logger = slog.New(flightEventHandler{
				Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}),
				handle: func(r slog.Record) {
					switch r.Message {
					case "cache_capacity_wait", "cache_task_attached":
						events <- r.Message
					case "block_demand":
						if !pauseBlock {
							return
						}
						// Pause after the handoff lease reaches the scheduler. Its
						// expiry must cancel the body before the coordinator can
						// retire the flight, independent of timer/goroutine order.
						r.Attrs(func(a slog.Attr) bool {
							if a.Key == "handoff_sources" && a.Value.Int64() > 0 {
								<-coordinatorGate
							}
							return true
						})
					}
				},
			})
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
						body = io.NopCloser(publishReaderFunc(func([]byte) (int, error) {
							<-r.Context().Done()
							// Let asynchronous Close enter cleanup before Read
							// returns and the executor reports its terminal result.
							<-gate
							return 0, r.Context().Err()
						}))
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
			if !pauseBlock {
				// This case commits session retirement, unlike a resumable
				// block pause. Neither response kind may accept another GET.
				s.mu.Lock()
				old.preempted = true
				old.cancel(errPreempted)
				s.mu.Unlock()
			}
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
						body, err = io.ReadAll(reader)
						reader.Close()
					} else {
						body, err = os.ReadFile(f.path)
					}
				}
				result <- outcome{f, body, err}
			}()
			// A paused 206 may resume in the same session; a canceled 200 has
			// no safe restart and must wait for a replacement session.
			reuse := pauseBlock && status == http.StatusPartialContent
			wantEvent := "cache_capacity_wait"
			if reuse {
				wantEvent = "cache_task_attached"
			}
			if event := receiveDemandEvent(t, events); event != wantEvent {
				t.Errorf("new GET admission = %s, want %s", event, wantEvent)
			}
			if requests.Load() != 1 {
				t.Error("replacement started before old cleanup finished")
			}
			resumeCoordinator()
			unblock()
			got := receiveDemandEvent(t, result)
			if got.err != nil || (got.flight == old) != reuse || string(got.body) != payload {
				t.Fatalf("GET did not recover after cleanup: old=%v bytes=%d error=%v", got.flight == old, len(got.body), got.err)
			}
			<-got.flight.done // Delivery does not wait for validation/publication.
			if body, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(body) != payload {
				t.Fatal("replacement was not verified and published", err)
			}
		})
	}
}
