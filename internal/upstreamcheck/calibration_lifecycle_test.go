package upstreamcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCalibrationCyclesVideoThroughRealHTTP(t *testing.T) {
	ranges := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "play.udon.dance" {
			t.Errorf("unexpected resource host: %s", r.Host)
		}
		select {
		case ranges <- r.Header.Get("Range"):
		default:
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", calibrationBlock-1, calibrationBlock))
		w.Header().Set("Content-Length", fmt.Sprint(calibrationBlock))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.CopyN(w, zeroCalibrationReader{}, calibrationBlock)
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	c := newTestCalibration(client, "direct", func(context.Context) CalibrationCandidates {
		return calibrationSavedCandidates(1, calibrationBlock)
	}, nil)
	c.limits.reserveVideos = 1
	defer c.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		select {
		case actual := <-ranges:
			if actual != fmt.Sprintf("bytes=0-%d", calibrationBlock-1) {
				t.Fatalf("unexpected repeated range: %q", actual)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("completed video was not requested again")
		}
	}
	c.Cancel()
	s := waitCalibrationResult(c)
	if s.Phase != "canceled" || s.Bytes < 2*calibrationBlock || s.Failures != 0 {
		t.Fatalf("real HTTP reload or cancellation failed: %+v", s)
	}
}

func TestCalibrationStartsAtSixteenVideosOrPreparationDeadline(t *testing.T) {
	for _, count := range []int{0, 3, 16} {
		t.Run(map[int]string{0: "empty", 3: "partial", 16: "ready"}[count], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var downloads atomic.Int32
				client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
					downloads.Add(1)
					<-r.Context().Done()
					return nil, r.Context().Err()
				}))
				c := newTestCalibration(client, "direct", func(context.Context) CalibrationCandidates {
					return calibrationSavedCandidates(count, 1<<20)
				}, nil)
				defer c.Close()
				at := time.Now()
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				state, _ := c.Snapshot()
				if count == 16 {
					if state.Phase != "running" || state.PreparedVideos != 16 || downloads.Load() == 0 || time.Since(at) != 0 {
						t.Fatalf("16 small videos did not start immediately: %+v", state)
					}
				} else {
					if state.Phase != "preparing" || state.PreparedVideos != count || downloads.Load() != 0 {
						t.Fatalf("partial reserve started too soon: %+v", state)
					}
					time.Sleep(29 * time.Second)
					synctest.Wait()
					state, _ = c.Snapshot()
					if state.Phase != "preparing" || downloads.Load() != 0 {
						t.Fatalf("preparation did not wait for 30 seconds: %+v", state)
					}
					time.Sleep(time.Second)
					synctest.Wait()
					state, _ = c.Snapshot()
					if count == 0 {
						if state.Phase != "failed" || state.Reason != "preparation_insufficient" || downloads.Load() != 0 {
							t.Fatalf("empty preparation did not fail at deadline: %+v", state)
						}
					} else if state.Phase != "running" || downloads.Load() == 0 {
						t.Fatalf("partial preparation did not start at deadline: %+v", state)
					}
				}
				c.Cancel()
				c.Close()
			})
		})
	}
}

func TestCalibrationWaitsForMinimumAfterBothSpeedWindows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		candidates := calibrationSavedCandidates(16, 128<<20)
		c := newTestCalibration(requestClient(transportFunc(calibrationPacedResource)), "direct", func(context.Context) CalibrationCandidates {
			return candidates
		}, nil)
		defer c.Close()
		c.limits.warmup, c.limits.window = 100*time.Millisecond, 100*time.Millisecond
		at := time.Now()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		for {
			s, changed := c.Snapshot()
			if !s.Running {
				t.Fatalf("measurement ended before the minimum: %+v", s)
			}
			if s.MeasurementStage == "complete" {
				if time.Since(at) >= 10*time.Second || s.SingleSpeedBPS == nil || s.FullSpeedBPS == nil {
					t.Fatalf("fixture did not finish both windows before the minimum: %+v", s)
				}
				break
			}
			<-changed
		}
		time.Sleep(10*time.Second - time.Since(at))
		s := waitCalibrationResult(c)
		if s.Phase != "completed" || s.Reason != "measurement_complete" || time.Since(at) != 10*time.Second || s.LastValid == nil {
			t.Fatalf("measurement did not end at its minimum: elapsed=%s state=%+v", time.Since(at), s)
		}
	})
}

func TestCalibrationStopsAtMaximumWithoutFullConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		candidates := calibrationSavedCandidates(1, 1<<20)
		c := newTestCalibration(requestClient(transportFunc(calibrationPacedResource)), "direct", func(context.Context) CalibrationCandidates {
			return candidates
		}, nil)
		defer c.Close()
		oldSpeed := 123.0
		c.saved = &CalibrationResult{Run: 7, FullSpeedBPS: &oldSpeed}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		for {
			s, changed := c.Snapshot()
			if s.Phase == "running" {
				break
			}
			if !s.Running {
				t.Fatalf("measurement did not start: %+v", s)
			}
			<-changed
		}
		started := time.Now()
		s := waitCalibrationResult(c)
		if s.Phase != "completed" || s.Reason != "time_limit" || time.Since(started) != 120*time.Second || s.Bytes == 0 || s.FullSpeedBPS != nil || s.LastValid == nil || s.LastValid.Run != 7 {
			t.Fatalf("maximum or previous result changed: elapsed=%s state=%+v", time.Since(started), s)
		}
	})
}
