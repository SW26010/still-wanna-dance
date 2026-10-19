package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestLeaseStopCompletesBeforeCoordinatorRetirement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		task, _, err := s.scheduler.Schedule("old", 1, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
			<-ctx.Done()
			return blocksched.Result{Err: ctx.Err()}
		}, blocksched.Need{Owner: "owner", LeaseUntil: time.Now().Add(time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		w.jobs[0] = &scheduledBlock{task: task}
		time.Sleep(time.Second)
		<-task.Done()
		initialized := true
		if whole, err := w.reapCompleted(&initialized); whole || err != nil || len(w.jobs) != 0 {
			t.Fatal("completed lease retirement failed a newly attached GET", whole, err, len(w.jobs))
		}
	})
}

func TestCanceledSessionCannotRestartTask(t *testing.T) {
	for _, headers := range []bool{true, false} {
		name := "partial_body"
		if headers {
			name = "waiting_headers"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := setup(t, nil)
			body := bytes.Repeat([]byte("x"), int(rangeBlockSize))
			r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
			v, _ := s.parseResolved(r)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			f := &flight{streaming: make(chan struct{}), log: s.cfg.Logger}
			f.progress = startProgress(f.log, v.size, time.Minute)
			w := newDownloadSession(s, ctx, v, f)
			defer func() {
				f.progress.finish(nil)
				if f.spool != nil {
					f.spool.release()
				}
				if f.ranges != nil {
					f.ranges.storage.release()
				}
			}()
			ready := make(chan int64, 4)
			var calls, runs atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					if headers {
						ready <- 0
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: 206, ContentLength: rangeBlockSize, Request: r,
						Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", rangeBlockSize-1, v.size)}},
						Body:   &yieldingRangeBody{ctx: r.Context(), prefix: body[:64<<10], ready: ready}}, nil
				})}
			}
			// Leave the scheduler's demand valid, modeling a coordinator that has
			// not processed the session cancellation yet. It must not restart Run.
			task, _, err := s.scheduler.Schedule("cancel-test", rangeBlockSize, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
				runs.Add(1)
				return w.run(ctx, p, 0)
			}, blocksched.Need{Owner: "video", LeaseUntil: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { task.Close(); <-task.Done() }()

			receiveDemandEvent(t, ready)
			cancel(errPreempted)
			receiveDemandEvent(t, task.Done())
			if !errors.Is(task.Err(), errPreempted) || calls.Load() != 1 || runs.Load() != 1 {
				t.Fatal("canceled session remained restartable", task.Err(), calls.Load(), runs.Load())
			}
			// A dispatch already queued when the session ended also avoids HTTP.
			if result := w.run(context.Background(), new(blocksched.Progress), 0); !errors.Is(result.Err, errPreempted) || calls.Load() != 1 {
				t.Fatal("late dispatch opened a canceled session", result.Err, calls.Load())
			}
			if !headers && (f.ranges.bytes() != 0 || f.ranges.has(0)) {
				t.Fatal("cancellation exposed an incomplete block", f.ranges.bytes())
			}
		})
	}
}
