package upstreamcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/playbacksource"
)

const calibrationBlock = cacheproxy.DefaultBlockSize

const calibrationFixture = "https://play.udon.dance/files/123/42-abc.mp4?e=58f06dd588d8ffb3beb46ada6309436b&s=33554432&secret=private-test-token"

func newTestCalibration(client *http.Client, upstream string, candidates func(context.Context) CalibrationCandidates, observe func(context.Context, PlaybackURLResponse)) *Calibration {
	return NewCalibration(client, upstream, CalibrationOptions{Candidates: candidates, OnPlaybackURL: observe,
		Playback: func(ctx context.Context, id int64, route playbacksource.Entry) (Result, *Sample, PlaybackURLResponse) {
			return ProbePlayback(ctx, []*http.Client{client}, DefaultPolicy(), id, route)
		},
	})
}

func calibrationTransport(resource func(*http.Request) (*http.Response, error)) http.RoundTripper {
	return transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/Api/Songs/list":
			return response(200, `{"groups":{"contents":[{"songInfos":[{"id":42}]}]}}`), nil
		case "/Api/Songs/play":
			if r.URL.Query().Get("node") != "cf" {
				return nil, fmt.Errorf("unexpected node")
			}
			v := response(302, "")
			v.Header.Set("Location", calibrationFixture)
			return v, nil
		default:
			return resource(r)
		}
	})
}

func calibrationForTest(t *testing.T, transport http.RoundTripper, songCount ...int) *Calibration {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	t.Setenv("TMPDIR", root)
	count := 1
	if len(songCount) != 0 {
		count = songCount[0]
	}
	ids := make([]int64, count)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	c := newTestCalibration(requestClient(transport), "direct", func(context.Context) CalibrationCandidates { return CalibrationCandidates{SongIDs: ids} }, nil)
	// Download-focused fixtures intentionally contain one short video. Tests
	// of production preparation restore the real reserve requirements below.
	c.limits.reserveVideos, c.limits.minimum = 1, 0
	t.Cleanup(func() {
		c.Close()
		paths, err := filepath.Glob(filepath.Join(root, "swd-calibration-*"))
		if err != nil || len(paths) != 0 {
			t.Errorf("diagnostic storage leaked: %v %v", paths, err)
		}
	})
	return c
}

func awaitCalibration(t *testing.T, c *Calibration, accept func(CalibrationStatus) bool) CalibrationStatus {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		s, changed := c.Snapshot()
		if accept(s) {
			return s
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("calibration timed out: %+v", s)
		}
	}
}

func TestCalibrationFailureAndCancellationPreserveTestTimes(t *testing.T) {
	var fail atomic.Bool
	c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if fail.Load() {
			return response(http.StatusServiceUnavailable, ""), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	c.limits.prepare = 100 * time.Millisecond
	var finished time.Time
	for _, failure := range []bool{false, true, false} {
		fail.Store(failure)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		if !failure {
			c.Cancel()
		}
		s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
		if failure {
			if s.Phase != "failed" || s.Finished.IsZero() {
				t.Fatalf("failure lost its completion time: %+v", s)
			}
			finished = s.Finished
		} else if s.Phase != "canceled" || !s.Finished.Equal(finished) {
			t.Fatalf("cancellation changed last completion time: got=%v want=%v phase=%s", s.Finished, finished, s.Phase)
		}
		if s.LastValid != nil {
			t.Fatal("failure or cancellation created a valid speed result")
		}
	}
}

type calibrationBlockedBody struct {
	ctx    context.Context
	close  func()
	once   sync.Once
	seeded bool
}

func (b *calibrationBlockedBody) Read(p []byte) (int, error) {
	if !b.seeded {
		b.seeded = true
		p[0] = 0
		return 1, nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *calibrationBlockedBody) Close() error { b.once.Do(b.close); return nil }

func calibrationRange(r *http.Request, body io.ReadCloser) *http.Response {
	return calibrationRangeSized(r, body, 32<<20)
}

func calibrationRangeSized(r *http.Request, body io.ReadCloser, size int64) *http.Response {
	var start, end int64
	fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
	return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, size)}}, ContentLength: end - start + 1, Body: body}
}

func TestCalibrationHoldsSingleThenUsesNaturalGrowthAndCancels(t *testing.T) {
	// Keep the admission cadence deterministic: host scheduling can delay one
	// transport callback and compress the observed gap to the next callback.
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var starts []time.Time
		var active atomic.Int32
		allStarted := make(chan struct{})
		c := calibrationForTest(t, calibrationTransport(func(r *http.Request) (*http.Response, error) {
			active.Add(1)
			mu.Lock()
			starts = append(starts, time.Now())
			if len(starts) == 16 {
				close(allStarted)
			}
			mu.Unlock()
			return calibrationRange(r, &calibrationBlockedBody{ctx: r.Context(), close: func() { active.Add(-1) }}), nil
		}))
		c.limits.warmup, c.limits.window, c.limits.interval = 100*time.Millisecond, time.Second, 20*time.Millisecond
		initial, changed := c.Snapshot()
		if initial.Running || initial.Phase != "idle" || active.Load() != 0 {
			t.Fatal("subscription started work")
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		<-changed
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-allStarted:
		case <-time.After(7 * time.Second):
			t.Fatal("ordinary prefetch window did not fill")
		}
		// The diagnostic releases the ceiling; the original scheduler still grows.
		time.Sleep(300 * time.Millisecond)
		s, _ := c.Snapshot()
		if s.Run != 1 || s.PeakActive != 16 || s.PeakOpen != 16 {
			t.Fatalf("changed natural window: %+v", s)
		}
		c.Cancel()
		s = awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
		if s.Phase != "canceled" || s.Failures != 0 || active.Load() != 0 {
			t.Fatalf("cancellation did not drain: %+v active=%d", s, active.Load())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(starts) != 16 {
			t.Fatalf("requests = %d", len(starts))
		}
		if starts[1].Sub(starts[0]) < time.Second {
			t.Fatal("released before single sampling finished")
		}
		for i := 1; i < len(starts); i++ {
			if gap := starts[i].Sub(starts[i-1]); gap < 200*time.Millisecond {
				t.Fatalf("request %d bypassed pacing: %s", i, gap)
			}
		}
	})
}

func TestCalibrationLoopsDiscardedVideoAndSnapshots(t *testing.T) {
	var mu sync.Mutex
	ranges := map[string]int{}
	block := make([]byte, calibrationBlock)
	c := calibrationForTest(t, calibrationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != strings.Replace(calibrationFixture, "https://", "http://", 1) || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("changed fixed video or encoding")
		}
		mu.Lock()
		ranges[r.Header.Get("Range")]++
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // Keep full-block samples above the platform clock resolution.
		return calibrationRange(r, io.NopCloser(bytes.NewReader(block))), nil
	}))
	// A short source is reread until the time limit, not treated as exhausted.
	c.limits.interval, c.limits.window, c.limits.duration = 10*time.Millisecond, time.Hour, 300*time.Millisecond
	for run := uint64(1); run <= 2; run++ {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
		if s.Run != run || s.Phase != "completed" || s.Reason != "time_limit" || s.Bytes <= 32<<20 || s.Completed <= 32 || s.BlockTimeMS <= 0 || len(s.Samples) == 0 || s.SingleSpeedBPS != nil || s.FullSpeedBPS != nil {
			t.Fatalf("short video did not loop until the time limit: %+v", s)
		}
		mu.Lock()
		if len(ranges) != 32 {
			t.Errorf("ranges = %d", len(ranges))
		}
		for key, count := range ranges {
			if count <= int(run) {
				t.Errorf("range was not reread from upstream: %s = %d", key, count)
			}
		}
		mu.Unlock()
		data, _ := json.Marshal(s)
		if strings.Contains(string(data), "private-test-token") || strings.Contains(string(data), "https://") {
			t.Fatal("snapshot exposed URL")
		}
		s.Samples[0].SpeedBPS = -123
		fresh, _ := c.Snapshot()
		if fresh.Samples[0].SpeedBPS < 0 {
			t.Fatal("snapshot aliases state")
		}
	}
}

func TestCalibrationSequentialFallbackDoesNotCheckIntegrity(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			data := make([]byte, 32<<20)
			if corrupt {
				data[0] = 1
			}
			var requests atomic.Int32
			repeated := make(chan struct{})
			c := calibrationForTest(t, calibrationTransport(func(r *http.Request) (*http.Response, error) {
				if requests.Add(1) == 2 {
					close(repeated)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data))}, nil
			}))
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			<-repeated
			c.Cancel()
			s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
			if s.Phase != "canceled" || s.PeakActive != 1 || s.Completed != 0 || requests.Load() < 2 {
				t.Fatalf("sequential/integrity behavior changed: %+v requests=%d", s, requests.Load())
			}
		})
	}
}

func TestCalibrationDoesNotStopAtOneGiB(t *testing.T) {
	reached := make(chan struct{})
	fixture, _ := calibrationManyVideosSized(t, 1, 2<<30, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: 2 << 30,
			Body: io.NopCloser(&calibrationThresholdReader{ctx: r.Context(), reached: reached})}, nil
	})
	c := calibrationForTest(t, fixture)
	c.limits.minimum = 10 * time.Second
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("did not read past the former byte limit")
	}
	s, _ := c.Snapshot()
	if !s.Running || c.bytes.Load() <= 1<<30 {
		t.Fatalf("measurement stopped at the former byte limit: %+v", s)
	}
	c.Cancel()
	awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
}

type calibrationThresholdReader struct {
	ctx     context.Context
	reached chan struct{}
	read    int64
}

func (r *calibrationThresholdReader) Read(p []byte) (int, error) {
	if r.read > 1<<30 {
		close(r.reached)
		<-r.ctx.Done()
		return 0, r.ctx.Err()
	}
	clear(p)
	r.read += int64(len(p))
	return len(p), nil
}

func TestCalibrationRangeValidation(t *testing.T) {
	for _, kind := range []string{"wrong_range", "short", "encoding"} {
		t.Run(kind, func(t *testing.T) {
			var closed atomic.Int32
			c := calibrationForTest(t, calibrationTransport(func(r *http.Request) (*http.Response, error) {
				resp := calibrationRange(r, &calibrationTestBody{Reader: bytes.NewReader([]byte{0}), closed: &closed})
				if kind == "wrong_range" {
					resp.Header.Set("Content-Range", "bytes 0-1048575/999")
				}
				if kind == "encoding" {
					resp.Header.Set("Content-Encoding", "gzip")
				}
				return resp, nil
			}))
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
			if s.Phase != "failed" || closed.Load() == 0 {
				t.Fatalf("invalid body accepted/leaked: %+v", s)
			}
		})
	}
}

type calibrationTestBody struct {
	io.Reader
	closed *atomic.Int32
}

func (b *calibrationTestBody) Close() error { b.closed.Add(1); return nil }

func TestCalibrationTimeLimitAndRetirement(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprint(retire), func(t *testing.T) {
			var requests atomic.Int32
			c := calibrationForTest(t, calibrationTransport(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}))
			c.limits.duration = 50 * time.Millisecond
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			if retire {
				c.Shutdown()
			}
			s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
			if retire && s.Phase != "canceled" || !retire && (s.Phase != "failed" || s.Reason != "time_limit" || requests.Load() != 1) {
				t.Fatalf("boundary: %+v requests=%d", s, requests.Load())
			}
			c.Shutdown()
			if err := c.Start(); err == nil {
				t.Fatal("closed calibration restarted")
			}
		})
	}
}

func TestCalibrationDoesNotNeedWritableTemporaryStorage(t *testing.T) {
	var requests atomic.Int32
	repeated := make(chan struct{})
	c := calibrationForTest(t, calibrationTransport(func(*http.Request) (*http.Response, error) {
		if requests.Add(1) == 2 {
			close(repeated)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: 32 << 20,
			Body: io.NopCloser(io.LimitReader(zeroCalibrationReader{}, 32<<20))}, nil
	}))
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", file)
	t.Setenv("TEMP", file)
	t.Setenv("TMPDIR", file)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	<-repeated
	s, _ := c.Snapshot()
	if !s.Running || c.bytes.Load() < 32<<20 {
		t.Fatalf("discard depended on storage: %+v", s)
	}
	c.Cancel()
	awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
}

type zeroCalibrationReader struct{}

func (zeroCalibrationReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

type pacedCalibrationBody struct {
	ctx       context.Context
	remaining int64
	active    *atomic.Int32
	once      sync.Once
}

func (b *pacedCalibrationBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	timer := time.NewTimer(2 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-timer.C:
	}
	n := min(int64(len(p)), b.remaining)
	clear(p[:n])
	b.remaining -= n
	return int(n), nil
}
func (b *pacedCalibrationBody) Close() error { b.once.Do(func() { b.active.Add(-1) }); return nil }

func TestCalibrationMultipleVideosFinishBothMeasurementStages(t *testing.T) {
	synctest.Test(t, calibrationMultipleVideosFinishBothMeasurementStages)
}

func calibrationMultipleVideosFinishBothMeasurementStages(t *testing.T) {
	var mu sync.Mutex
	ranges := map[string]int{}
	var active, catalogs atomic.Int32
	c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/Api/Songs/list":
			catalogs.Add(1)
			return response(200, `{"groups":{"contents":[{"songInfos":[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5},{"id":6},{"id":7},{"id":8},{"id":9}]}]}}`), nil
		case "/Api/Songs/play":
			var id int
			fmt.Sscanf(r.URL.Query().Get("id"), "%d", &id)
			if id == 9 {
				id = 1
			} // Two songs share the same resource; never load it twice.
			v := response(302, "")
			v.Header.Set("Location", fmt.Sprintf("https://play.udon.dance/files/123/%d-abc.mp4?e=%032x&s=33554432", id, id))
			return v, nil
		default:
			mu.Lock()
			ranges[r.URL.String()+r.Header.Get("Range")]++
			mu.Unlock()
			active.Add(1)
			resp := calibrationRange(r, nil)
			resp.Body = &pacedCalibrationBody{ctx: r.Context(), remaining: resp.ContentLength, active: &active}
			return resp, nil
		}
	}), 9)
	c.limits.warmup, c.limits.window, c.limits.interval = 50*time.Millisecond, 100*time.Millisecond, 10*time.Millisecond
	c.limits.reserveVideos = 8
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if s.Phase != "completed" || s.Reason != "measurement_complete" || s.SingleSpeedBPS == nil || s.FullSpeedBPS == nil || *s.SingleSpeedBPS <= 0 || *s.FullSpeedBPS < 3**s.SingleSpeedBPS || s.PeakActive != 16 || s.PeakOpen != 16 || active.Load() != 0 {
		t.Fatalf("stages did not finish/drain: %+v active=%d", s, active.Load())
	}
	if s.ConcurrencyLimit != 16 {
		t.Fatal("did not reach the ordinary concurrency ceiling")
	}
	if _, available := calibrationCPUTime(); available && (s.CPUPercent == nil || s.CPUP95Percent == nil || s.CPUP99Percent == nil || *s.CPUP99Percent < *s.CPUP95Percent) {
		t.Fatal("missing/invalid process CPU statistics")
	}
	mu.Lock()
	if catalogs.Load() != 0 {
		t.Fatalf("measurement requested a catalog: %d", catalogs.Load())
	}
	if len(ranges) == 0 {
		t.Fatal("no video range was requested")
	}
	mu.Unlock()
	previous := s.LastValid
	if s.Finished.IsZero() || !previous.Finished.Equal(s.Finished) || previous.Run != s.Run || previous.FullSpeedBPS == nil || *previous.FullSpeedBPS != *s.FullSpeedBPS || previous.FullConcurrency != 16 || s.LiveSpeedBPS == nil {
		t.Fatal("completed measurement not retained")
	}
	if s.CPUP95Percent != nil && (previous.CPUP95Percent == nil || *previous.CPUP95Percent != *s.CPUP95Percent) {
		t.Fatal("completed CPU P95 not retained")
	}
	// Retrying and canceling cannot replace a completed observation, including
	// a fresh event subscription that never saw the previous run finish.
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	c.Cancel()
	after := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if after.Phase != "canceled" || !after.Finished.Equal(s.Finished) || after.LastValid.Run != previous.Run || *after.LastValid.FullSpeedBPS != *previous.FullSpeedBPS {
		t.Fatal("canceled run lost previous measurement")
	}
	if previous.CPUP95Percent != nil && (after.LastValid.CPUP95Percent == nil || *after.LastValid.CPUP95Percent != *previous.CPUP95Percent) {
		t.Fatal("canceled run lost previous CPU P95")
	}
	after.LastValid.Run++
	if current, _ := c.Snapshot(); current.LastValid.Run != previous.Run {
		t.Fatal("snapshot aliases retained result map")
	}
}

func calibrationManyVideos(t *testing.T, count int, resource func(*http.Request) (*http.Response, error)) (http.RoundTripper, *atomic.Int32) {
	t.Helper()
	return calibrationManyVideosSized(t, count, 16<<20, resource)
}

func calibrationManyVideosSized(t *testing.T, count int, size int64, resource func(*http.Request) (*http.Response, error)) (http.RoundTripper, *atomic.Int32) {
	t.Helper()
	var catalogs atomic.Int32
	songs := make([]map[string]int, count)
	for i := range songs {
		songs[i] = map[string]int{"id": i + 1}
	}
	encoded, err := json.Marshal(map[string]any{"groups": map[string]any{"contents": []any{map[string]any{"songInfos": songs}}}})
	if err != nil {
		t.Fatal(err)
	}
	return transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/Api/Songs/list":
			catalogs.Add(1)
			return response(200, string(encoded)), nil
		case "/Api/Songs/play":
			var id int
			fmt.Sscanf(r.URL.Query().Get("id"), "%d", &id)
			v := response(302, "")
			v.Header.Set("Location", fmt.Sprintf("https://play.udon.dance/files/123/%d-abc.mp4?e=%032x&s=%d", id, id, size))
			return v, nil
		default:
			return resource(r)
		}
	}), &catalogs
}

func TestCalibrationPreparationStopsAtVideoCount(t *testing.T) {
	transport, catalogs := calibrationManyVideos(t, 160, func(*http.Request) (*http.Response, error) {
		t.Error("preparation downloaded media")
		return nil, context.Canceled
	})
	c := calibrationForTest(t, transport, 160)
	c.limits.reserveVideos = 16
	client, samples, err := c.prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	var size int64
	seen := map[string]bool{}
	for _, sample := range samples {
		if seen[sample.checksum] {
			t.Fatal("duplicate resource in reserve")
		}
		seen[sample.checksum] = true
		size += sample.size
	}
	if len(samples) != 16 || size != 16*16<<20 || catalogs.Load() != 0 {
		t.Fatalf("preparation did not stop at 16 videos: count=%d size=%d catalogs=%d", len(samples), size, catalogs.Load())
	}
}

func TestCalibrationCyclesPreparedVideos(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		requests := map[string]int{}
		var active atomic.Int32
		transport, catalogs := calibrationManyVideosSized(t, 12, calibrationBlock, func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			requests[r.URL.Query().Get("e")]++
			mu.Unlock()
			active.Add(1)
			resp := calibrationRangeSized(r, nil, calibrationBlock)
			resp.Body = &pacedCalibrationBody{ctx: r.Context(), remaining: resp.ContentLength, active: &active}
			return resp, nil
		})
		c := calibrationForTest(t, transport, 12)
		c.limits.window, c.limits.duration, c.limits.reserveVideos = time.Minute, 8*time.Second, 12
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		s := waitCalibrationResult(c)
		if s.Phase != "completed" || s.Reason != "time_limit" || s.Bytes <= 12*calibrationBlock || s.SingleSpeedBPS != nil || s.FullSpeedBPS != nil || active.Load() != 0 {
			t.Fatalf("prepared videos did not loop until timeout: %+v", s)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requests) != 12 || catalogs.Load() != 0 {
			t.Fatalf("did not use the complete prepared pool: videos=%d catalogs=%d", len(requests), catalogs.Load())
		}
		for _, n := range requests {
			if n < 2 {
				t.Fatal("prepared video was not requested again", requests)
			}
		}
	})
}

func TestCalibrationPreparationDoesNotSampleCPU(t *testing.T) {
	started := make(chan struct{}, 1)
	c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	// More than two CPU sample intervals: preparation must still have no CPU data.
	<-time.After(250 * time.Millisecond)
	state, _ := c.Snapshot()
	if state.Phase != "preparing" || state.CPUP99Percent != nil || state.CPUPercent != nil || state.FullCPUPercent != nil {
		t.Fatal("preparation polluted download CPU statistics")
	}
	c.Cancel()
	awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
}
