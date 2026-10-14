package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// The demand is inspected during planning. Let the last executor finish at
// that point, after the coordinator could have taken a completion snapshot.
// This barrier is confined to the test context, with no production hooks.
type completeDuringPlanContext struct {
	context.Context
	streaming <-chan struct{}
	complete  func()
	once      sync.Once
}

func (c *completeDuringPlanContext) Err() error {
	select {
	case <-c.streaming:
		c.once.Do(c.complete)
	default:
	}
	return c.Context.Err()
}

func TestLastRangeCompletingDuringPlanStillVerifiesAndPublishes(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "checksum_failure"}[corrupt], func(t *testing.T) {
			s, cfg := setup(t, nil)
			body := bytes.Repeat([]byte("0123456789abcdef"), int(rangeBlockSize/16))
			r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
			v, err := s.parseResolved(r)
			if err != nil {
				t.Fatal(err)
			}
			upstream := append([]byte(nil), body...)
			if corrupt {
				upstream[len(upstream)-1] ^= 1
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			gate, finished := make(chan struct{}), make(chan struct{})
			s.scheduler.Close()
			s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1, Observe: func(e blocksched.Event) {
				if e.Kind == "finished" {
					close(finished)
				}
			}})
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 206, ContentLength: int64(len(upstream)), Request: r,
						Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", len(upstream)-1, len(upstream))}},
						Body:   &gatedFirstRange{ctx: r.Context(), gate: gate, Reader: bytes.NewReader(upstream)}}, nil
				})}
			}
			f := &flight{streaming: make(chan struct{}), log: s.cfg.Logger}
			f.progress = startProgress(f.log, v.size, time.Minute)
			planCtx := &completeDuringPlanContext{Context: context.Background(), streaming: f.streaming, complete: func() {
				close(gate)
				select {
				case <-finished:
				case <-ctx.Done():
				}
			}}
			_, release := s.addDemand(planCtx, v, true)
			defer release()
			s.pinVideo(v)
			defer s.releaseVideo(v)
			done := make(chan struct{})
			var result error
			go func() {
				defer close(done)
				result = s.publishScheduled(ctx, cfg.videoFile(v.key), v, f)
				f.progress.finish(result)
				if f.spool != nil {
					f.spool.finish(result) // Same terminal propagation as the flight owner.
				}
			}()
			defer func() {
				cancel()
				<-done
				if f.spool != nil {
					f.spool.release()
				}
				if f.ranges != nil {
					f.ranges.storage.release()
				}
			}()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("last range did not finish during planning")
			}
			reader := f.spool.reader(ctx, v.size)
			defer reader.Close()
			reader.verifyFull = true
			got, readErr := io.ReadAll(reader)
			<-done
			published, fileErr := os.ReadFile(cfg.videoFile(v.key))
			if corrupt {
				if !errors.Is(result, errUpstreamDownload) || !errors.Is(readErr, errUpstreamDownload) || !os.IsNotExist(fileErr) {
					t.Fatalf("checksum was not enforced: result=%v read=%v file=%v", result, readErr, fileErr)
				}
			} else if result != nil || readErr != nil || fileErr != nil || !bytes.Equal(got, body) || !bytes.Equal(published, body) {
				t.Fatalf("last range was lost: result=%v read=%v bytes=%d file=%v published=%d", result, readErr, len(got), fileErr, len(published))
			}
		})
	}
}
