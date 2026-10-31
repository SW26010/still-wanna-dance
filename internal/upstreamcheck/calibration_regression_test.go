package upstreamcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

type calibrationPacedRange struct {
	ctx       context.Context
	remaining int64
}

func (b *calibrationPacedRange) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	n := min(int64(len(p)), b.remaining)
	clear(p[:n])
	b.remaining -= n
	return int(n), nil
}
func (*calibrationPacedRange) Close() error { return nil }
func calibrationSavedCandidates(count int, size int64) CalibrationCandidates {
	var candidates CalibrationCandidates
	for id := 1; id <= count; id++ {
		candidates.URLs = append(candidates.URLs, cacheproxy.SongURL{SongID: int64(id), URL: fmt.Sprintf("http://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=%d", id, id, size)})
	}
	return candidates
}
func calibrationPacedResource(r *http.Request) (*http.Response, error) {
	var start, end int64
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
		return nil, err
	}
	size, _ := strconv.ParseInt(r.URL.Query().Get("s"), 10, 64)
	return &http.Response{StatusCode: 206, ContentLength: end - start + 1, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, size)}}, Body: &calibrationPacedRange{r.Context(), end - start + 1}}, nil
}
func waitCalibrationResult(c *Calibration) CalibrationStatus {
	for {
		s, changed := c.Snapshot()
		if !s.Running {
			return s
		}
		<-changed
	}
}

func TestCalibrationMeasuresAvailableReserve(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		size  int64
	}{{"one_video", 1, 2 << 30}, {"less_than_two_gib", 16, 16 << 20}, {"small_videos", 16, 8 << 20}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				candidates := calibrationSavedCandidates(tc.count, tc.size)
				c := newTestCalibration(requestClient(transportFunc(calibrationPacedResource)), "direct", func(context.Context) CalibrationCandidates { return candidates }, nil)
				defer c.Close()
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				s := waitCalibrationResult(c)
				if s.Reason != "measurement_complete" || s.SingleSpeedBPS == nil || s.FullSpeedBPS == nil || *s.SingleSpeedBPS <= 0 || *s.FullSpeedBPS <= 0 {
					t.Fatalf("normal measurement could not finish: phase=%s reason=%s bytes=%d", s.Phase, s.Reason, s.Bytes)
				}
			})
		})
	}
}

func TestCalibrationContinuesAfterResourceFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		candidates := calibrationSavedCandidates(16, 128<<20)
		var first sync.Once
		var failed string
		transport := transportFunc(func(r *http.Request) (*http.Response, error) {
			first.Do(func() { failed = r.URL.Query().Get("e") })
			if r.URL.Query().Get("e") == failed {
				return response(503, "unavailable"), nil
			}
			return calibrationPacedResource(r)
		})
		c := newTestCalibration(requestClient(transport), "direct", func(context.Context) CalibrationCandidates { return candidates }, nil)
		defer c.Close()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		s := waitCalibrationResult(c)
		if s.Phase != "completed" || s.Reason != "measurement_complete" || s.Failures != 1 {
			t.Fatalf("one resource terminated the whole run despite 15 healthy alternatives: phase=%s reason=%s failures=%d prepared=%d bytes=%d", s.Phase, s.Reason, s.Failures, s.PreparedVideos, s.Bytes)
		}
	})
}

func TestCalibrationNonRangeLoadsReachConfiguredConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		candidates := calibrationSavedCandidates(16, 128<<20)
		transport := transportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ContentLength: 128 << 20, Header: make(http.Header), Body: &calibrationPacedRange{r.Context(), 128 << 20}}, nil
		})
		c := newTestCalibration(requestClient(transport), "direct", func(context.Context) CalibrationCandidates { return candidates }, nil)
		defer c.Close()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		s := waitCalibrationResult(c)
		if s.Reason != "measurement_complete" || s.FullSpeedBPS == nil {
			t.Fatalf("healthy HTTP 200 resources cannot fill configured concurrency: phase=%s reason=%s prepared=%d bytes=%d peak=%d open=%d", s.Phase, s.Reason, s.PreparedVideos, s.Bytes, s.PeakActive, s.PeakOpen)
		}
	})
}
