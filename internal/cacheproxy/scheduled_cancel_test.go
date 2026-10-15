package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestCanceledSessionCannotRestartPausedTask(t *testing.T) {
	for _, headers := range []bool{true, false} {
		name := "partial_body"
		if headers {
			name = "waiting_headers"
		}
		t.Run(name, func(t *testing.T) {
			s, cfg := setup(t, nil)
			body := bytes.Repeat([]byte("x"), int(rangeBlockSize))
			r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
			v, _ := s.parseResolved(r)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			f := &flight{streaming: make(chan struct{}), log: s.cfg.Logger}
			f.progress = startProgress(f.log, v.size, time.Minute)
			w := &downloadSession{s: s, ctx: ctx, v: v, f: f, path: cfg.videoFile(v.key), initialized: make(chan struct{}), wake: make(chan struct{}, 1)}
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
			task, err := s.scheduler.Submit("cancel-test", rangeBlockSize, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
				runs.Add(1)
				return w.run(ctx, p, 0)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { task.Close(); <-task.Done() }()
			task.Add(blocksched.Need{Owner: "video", LeaseUntil: time.Now().Add(time.Minute)})
			receiveDemandEvent(t, ready)
			cancel(errPreempted)
			receiveDemandEvent(t, task.Done())
			if !errors.Is(task.Err(), errPreempted) || errors.Is(task.Err(), blocksched.ErrPaused) || calls.Load() != 1 || runs.Load() != 1 {
				t.Fatal("canceled session remained restartable", task.Err(), calls.Load(), runs.Load())
			}
			// A dispatch already queued when the session ended also avoids HTTP.
			if result := w.run(context.Background(), new(blocksched.Progress), 0); !errors.Is(result.Err, errPreempted) || calls.Load() != 1 {
				t.Fatal("late dispatch opened a canceled session", result.Err, calls.Load())
			}
			if !headers {
				if f.ranges.bytes() != 64<<10 {
					t.Fatal("session cancellation lost its saved prefix", f.ranges.bytes())
				}
				got := make([]byte, 64<<10)
				if _, err := io.ReadFull(io.NewSectionReader(f.ranges.storage.file, 0, 64<<10), got); err != nil || !bytes.Equal(got, body[:64<<10]) {
					t.Fatal("saved prefix differs", err)
				}
			}
		})
	}
}
