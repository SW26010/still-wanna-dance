package upstreamcheck

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCalibrationPreparationPreservesHTTPHeaderAndBodyBudgets(t *testing.T) {
	base, _ := calibrationManyVideos(t, 96, func(*http.Request) (*http.Response, error) {
		t.Error("preparation must not download video bodies")
		return nil, io.EOF
	})
	paths := transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.udon.dance" && !strings.HasPrefix(r.URL.Path, "/files/") {
			return response(http.StatusNotFound, ""), nil
		}
		if r.URL.Scheme != "http" {
			t.Fatal("unexpected protocol", r.URL)
		}
		delay := 350 * time.Millisecond
		if r.URL.Path == "/Api/Songs/list" {
			delay = 1600 * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		resp, err := base.RoundTrip(r)
		if resp != nil && r.URL.Path == "/Api/Songs/play" {
			resp.Header.Set("Location", strings.ReplaceAll(resp.Header.Get("Location"), "s=16777216", "s=33554432"))
		}
		if resp != nil && r.URL.Path == "/Api/Songs/list" {
			resp.Body = &preparationDelayedBody{ReadCloser: resp.Body, ctx: r.Context()}
		}
		return resp, err
	})
	c := calibrationForTest(t, paths)
	c.limits = NewCalibration(nil, "direct").limits
	synctest.Test(t, func(t *testing.T) {
		defer c.Close()
		ctx, cancel := context.WithTimeout(t.Context(), c.limits.prepare)
		defer cancel()
		at := time.Now()
		client, samples, err := c.prepare(ctx)
		if client != nil {
			defer client.CloseIdleConnections()
		}
		s, _ := c.Snapshot()
		if err != nil || len(samples) < c.limits.reserveVideos || s.PreparedBytes < c.limits.reserveBytes {
			t.Fatalf("usable metadata paths exhausted preparation: videos=%d bytes=%d elapsed=%s error=%v", s.PreparedVideos, s.PreparedBytes, time.Since(at), err)
		}
	})
}

type preparationDelayedBody struct {
	io.ReadCloser
	ctx     context.Context
	started bool
}

func (b *preparationDelayedBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		select {
		case <-time.After(3400 * time.Millisecond):
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	return b.ReadCloser.Read(p)
}

func TestCalibrationRejectsInsufficientReserveBeforeDownloading(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		size  string
	}{
		{"too_few_videos", 2, "2147483648"},
		{"too_few_bytes", 16, "16777216"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var media atomic.Int32
			base, _ := calibrationManyVideos(t, tc.count, func(*http.Request) (*http.Response, error) {
				media.Add(1)
				return nil, errors.New("unexpected media request")
			})
			c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.udon.dance" && !strings.HasPrefix(r.URL.Path, "/files/") {
					return response(404, ""), nil
				}
				resp, err := base.RoundTrip(r)
				if r.URL.Path == "/Api/Songs/play" && resp != nil {
					resp.Header.Set("Location", strings.ReplaceAll(resp.Header.Get("Location"), "s=16777216", "s="+tc.size))
				}
				return resp, err
			}))
			defaults := NewCalibration(nil, "direct").limits
			c.limits.reserveVideos, c.limits.reserveBytes = defaults.reserveVideos, defaults.reserveBytes
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
			if s.Phase != "failed" || s.Reason != "preparation_insufficient" || s.PreparedVideos != tc.count || s.PreparationProgress >= 100 || s.Bytes != 0 || media.Load() != 0 || s.CPUP99Percent != nil || s.SingleSpeedBPS != nil {
				t.Fatalf("undersized pool entered measurement: %+v media=%d", s, media.Load())
			}
		})
	}
}

func TestCalibrationPreparationParallelismAndDrain(t *testing.T) {
	base, _ := calibrationManyVideos(t, 160, func(*http.Request) (*http.Response, error) {
		t.Error("preparation downloaded media")
		return nil, context.Canceled
	})
	var active, peak atomic.Int32
	c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/play" {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		resp, err := base.RoundTrip(r)
		if r.URL.Path == "/Api/Songs/play" && resp != nil {
			resp.Header.Set("Location", strings.ReplaceAll(resp.Header.Get("Location"), "s=16777216", "s=134217728"))
		}
		return resp, err
	}))
	c.limits.reserveVideos, c.limits.reserveBytes = 16, 2<<30
	synctest.Test(t, func(t *testing.T) {
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		at := time.Now()
		client, samples, err := c.prepare(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		if len(samples) != 16 || time.Since(at) != 8*time.Second || peak.Load() != 4 || active.Load() != 0 {
			t.Fatalf("metadata pacing/drain: count=%d elapsed=%s peak=%d active=%d", len(samples), time.Since(at), peak.Load(), active.Load())
		}
	})
}

func TestCalibrationPreparationTimeoutAndCancelDrainPartialReserve(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelRun], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls, active atomic.Int32
				base, _ := calibrationManyVideos(t, 160, func(*http.Request) (*http.Response, error) {
					t.Error("partial preparation downloaded media")
					return nil, context.Canceled
				})
				c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/Api/Songs/play" && calls.Add(1) > 4 {
						active.Add(1)
						defer active.Add(-1)
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return base.RoundTrip(r)
				}))
				c.limits.reserveVideos, c.limits.reserveBytes, c.limits.prepare = 16, 2<<30, 200*time.Millisecond
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				if cancelRun {
					awaitCalibration(t, c, func(s CalibrationStatus) bool { return s.PreparedVideos == 4 })
					c.Cancel()
				}
				s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
				phase, reason := "failed", "preparation_insufficient"
				if cancelRun {
					phase, reason = "canceled", "canceled"
				}
				if s.Phase != phase || s.Reason != reason || s.PreparedVideos != 4 || active.Load() != 0 || s.Bytes != 0 || s.CPUP99Percent != nil {
					t.Fatalf("partial preparation leaked into download: %+v active=%d", s, active.Load())
				}
			})
		})
	}
}

func TestCalibrationPreparationContinuesPastShortCatalog(t *testing.T) {
	base, _ := calibrationManyVideos(t, 1, func(*http.Request) (*http.Response, error) { t.Error("media requested"); return nil, context.Canceled })
	c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v2/wanna/songs" {
			return response(200, `{"code":200,"data":{"time":"20261016000000","groups":[{"entries":[{"id":2,"checksum":"22222222222222222222222222222222"}]}]}}`), nil
		}
		return base.RoundTrip(r)
	}))
	c.limits.reserveVideos, c.limits.reserveBytes = 2, 32<<20
	client, samples, err := c.prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if len(samples) != 2 || samples[0].checksum == samples[1].checksum {
		t.Fatal("stopped at the first short catalog")
	}
}
