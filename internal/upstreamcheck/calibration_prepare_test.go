package upstreamcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestCalibrationPreparationUsesDefaultRequestBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/Api/Songs/play" || r.URL.Scheme != "http" {
				t.Error("preparation requested a catalog/media or changed protocol")
				return nil, io.EOF
			}
			select {
			case <-time.After(12 * time.Second):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			resp := response(302, "")
			resp.Header.Set("Location", calibrationFixture)
			return resp, nil
		}))
		ctx, cancel := context.WithTimeout(t.Context(), c.limits.prepare)
		defer cancel()
		at := time.Now()
		_, samples, err := c.prepare(ctx)
		if err != nil || len(samples) != 1 || time.Since(at) != 12*time.Second {
			t.Fatalf("request deadline was shortened: videos=%d elapsed=%s error=%v", len(samples), time.Since(at), err)
		}
	})
}

func TestCalibrationPreparesPartialReserveWithoutDownloading(t *testing.T) {
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
			}), tc.count)
			defaults := newTestCalibration(nil, "direct", nil, nil).limits
			c.limits.reserveVideos, c.limits.reserveBytes = defaults.reserveVideos, defaults.reserveBytes
			_, samples, err := c.prepare(t.Context())
			s, _ := c.Snapshot()
			if err != nil || len(samples) != tc.count || s.PreparedVideos != tc.count || media.Load() != 0 || s.CPUP99Percent != nil {
				t.Fatalf("partial preparation changed scope: videos=%d media=%d err=%v", len(samples), media.Load(), err)
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
	}), 160)
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
				base, _ := calibrationManyVideos(t, 160, func(r *http.Request) (*http.Response, error) {
					if active.Load() != 0 || cancelRun {
						t.Error("preparation was not drained before download")
					}
					return calibrationPacedResource(r)
				})
				c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/Api/Songs/play" && calls.Add(1) > 4 {
						active.Add(1)
						defer active.Add(-1)
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return base.RoundTrip(r)
				}), 160)
				c.limits.reserveVideos, c.limits.reserveBytes, c.limits.prepare = 16, 2<<30, 200*time.Millisecond
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				if cancelRun {
					awaitCalibration(t, c, func(s CalibrationStatus) bool { return s.PreparedVideos == 4 })
					c.Cancel()
				}
				s := waitCalibrationResult(c)
				phase, reason := "completed", "measurement_complete"
				if cancelRun {
					phase, reason = "canceled", "canceled"
				}
				if s.Phase != phase || s.Reason != reason || s.PreparedVideos != 4 || active.Load() != 0 || cancelRun && (s.Bytes != 0 || s.CPUP99Percent != nil) || !cancelRun && s.Bytes == 0 {
					t.Fatalf("partial preparation did not honor timeout/cancel: %+v active=%d", s, active.Load())
				}
			})
		})
	}
}

func TestCalibrationWithoutLocalSourceDoesNotDiscoverSongs(t *testing.T) {
	c := newTestCalibration(requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("missing local source triggered network discovery")
		return nil, io.EOF
	})), "direct", nil, nil)
	defer c.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if s.Phase != "failed" || s.Reason != "preparation_insufficient" || s.PreparedVideos != 0 || s.Bytes != 0 {
		t.Fatalf("empty local source: %+v", s)
	}
}

func TestCalibrationPreservesBusinessPriority(t *testing.T) {
	ids := []int64{9, 8, 7, 6, 5, 4, 3, 2, 1}
	started, release := make(chan int64, len(ids)), make(chan struct{})
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		started <- id
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		result := response(302, "")
		result.Header.Set("Location", calibrationFixture)
		return result, nil
	}))
	c := newTestCalibration(client, "direct", func(context.Context) CalibrationCandidates { return CalibrationCandidates{SongIDs: ids} }, nil)
	defer c.Close()
	c.limits.reserveVideos, c.limits.reserveBytes = 1, 16<<20
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := c.prepare(ctx); done <- err }()
	first := make([]int64, 0, calibrationPrepareWorkers)
	for range calibrationPrepareWorkers {
		select {
		case id := <-started:
			first = append(first, id)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	slices.Sort(first)
	if !slices.Equal(first, []int64{6, 7, 8, 9}) {
		t.Fatal("lower-priority songs displaced the first business candidates", first)
	}
}

func TestCalibrationJoinsURLObservationBeforeDownload(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var downloads atomic.Int32
				c := calibrationForTest(t, calibrationTransport(func(r *http.Request) (*http.Response, error) {
					downloads.Add(1)
					<-r.Context().Done()
					return nil, r.Context().Err()
				}))
				entered, release := make(chan struct{}), make(chan struct{})
				c.options.OnPlaybackURL = func(ctx context.Context, observation PlaybackURLResponse) {
					if observation.URL == "" || observation.QueryStartedAt.IsZero() || observation.ObservedAt.IsZero() {
						t.Error("incomplete playback observation")
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				<-entered
				synctest.Wait()
				state, _ := c.Snapshot()
				if downloads.Load() != 0 || state.Phase != "preparing" || state.CPUP99Percent != nil {
					t.Fatal("URL observation overlapped download measurement")
				}
				if !canceled {
					close(release)
					synctest.Wait()
					if downloads.Load() == 0 {
						t.Fatal("finishing the observation did not release preparation")
					}
				}
				c.Cancel()
				c.Close()
				if canceled && downloads.Load() != 0 {
					t.Fatal("canceled observation started a download")
				}
			})
		})
	}
}

func TestCalibrationTriesKnownSongsUntilExhausted(t *testing.T) {
	var calls atomic.Int32
	c := calibrationForTest(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/Api/Songs/play" {
			t.Error("exhausted local candidates triggered catalog discovery")
			return nil, io.EOF
		}
		calls.Add(1)
		return response(404, ""), nil
	}), 300)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if s.Phase != "failed" || calls.Load() != 300 {
		t.Fatalf("local candidates were truncated: phase=%s queries=%d", s.Phase, calls.Load())
	}
}

func TestCalibrationLocalCandidatesNeverFetchCatalog(t *testing.T) {
	for _, tc := range []struct {
		name       string
		local      []int64
		prepared   int
		unsuitable bool
	}{
		{name: "sufficient", local: []int64{4, 2, 3, 1, 2, 0, -1}, prepared: 4},
		{name: "empty"},
		{name: "partial", local: []int64{1, 1, 0, -1}, prepared: 1},
		{name: "unavailable", local: []int64{101, 102}},
		{name: "shared_and_small", local: []int64{1, 2, 3}, prepared: 2, unsuitable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := map[int64]int{}
			base, catalogs := calibrationManyVideos(t, 8, func(*http.Request) (*http.Response, error) {
				t.Error("preparation downloaded media")
				return nil, io.EOF
			})
			transport := transportFunc(func(r *http.Request) (*http.Response, error) {
				id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
				if r.URL.Path == "/Api/Songs/play" {
					mu.Lock()
					calls[id]++
					mu.Unlock()
					if id > 8 {
						return response(404, ""), nil
					}
				}
				resp, err := base.RoundTrip(r)
				if tc.unsuitable && r.URL.Path == "/Api/Songs/play" && resp != nil {
					if id == 2 {
						resp.Header.Set("Location", strings.ReplaceAll(resp.Header.Get("Location"), fmt.Sprintf("e=%032x", id), fmt.Sprintf("e=%032x", 1)))
					}
					if id == 3 {
						resp.Header.Set("Location", strings.ReplaceAll(resp.Header.Get("Location"), "s=16777216", "s=1048576"))
					}
				}
				return resp, err
			})
			original := slices.Clone(tc.local)
			c := newTestCalibration(requestClient(transport), "direct", func(context.Context) CalibrationCandidates { return CalibrationCandidates{SongIDs: tc.local} }, nil)
			defer c.Close()
			c.limits.reserveVideos, c.limits.reserveBytes = 4, 64<<20
			_, samples, err := c.prepare(t.Context())
			state, _ := c.Snapshot()
			insufficient := tc.prepared == 0
			bytes := int64(tc.prepared) * 16 << 20
			if tc.unsuitable {
				bytes = 17 << 20
			}
			if errors.Is(err, errCalibrationPreparationInsufficient) != insufficient || !insufficient && (err != nil || len(samples) != tc.prepared) || state.PreparedVideos != tc.prepared || state.PreparedBytes != bytes || catalogs.Load() != 0 || !slices.Equal(tc.local, original) {
				t.Fatalf("local reserve fetched a catalog or changed admission: videos=%d bytes=%d catalogs=%d local=%v error=%v", state.PreparedVideos, state.PreparedBytes, catalogs.Load(), tc.local, err)
			}
			for _, id := range tc.local {
				if id > 0 && calls[id] != 1 {
					t.Fatalf("local candidate %d was skipped or repeated: %v", id, calls)
				}
			}
			seen := map[string]bool{}
			for _, sample := range samples {
				if seen[sample.checksum] || sample.size <= 0 {
					t.Fatal("unsuitable media was counted in reserve")
				}
				seen[sample.checksum] = true
			}
		})
	}
}

func TestCalibrationLocalSelectionSharesDeadlineAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_%t", canceled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
					t.Error("local selection outlived the preparation context and reached network")
					return nil, io.EOF
				}))
				c := newTestCalibration(client, "direct", func(ctx context.Context) CalibrationCandidates {
					close(started)
					<-ctx.Done()
					return CalibrationCandidates{SongIDs: []int64{1}}
				}, nil)
				defer c.Close()
				at := time.Now()
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				<-started
				if canceled {
					c.Cancel()
				}
				for {
					state, changed := c.Snapshot()
					if !state.Running {
						phase, reason, elapsed := "failed", "preparation_insufficient", c.limits.prepare
						if canceled {
							phase, reason, elapsed = "canceled", "canceled", 0
						}
						if state.Phase != phase || state.Reason != reason || state.PreparedVideos != 0 || time.Since(at) != elapsed {
							t.Fatalf("selection lifetime: phase=%s reason=%s videos=%d elapsed=%s", state.Phase, state.Reason, state.PreparedVideos, time.Since(at))
						}
						break
					}
					<-changed
				}
			})
		})
	}
}

func TestCalibrationSavedURLsShareReserveAndFallback(t *testing.T) {
	var mu sync.Mutex
	calls := map[int64]int{}
	base, catalogs := calibrationManyVideos(t, 8, func(*http.Request) (*http.Response, error) {
		t.Error("preparation downloaded media")
		return nil, io.EOF
	})
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/play" {
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			mu.Lock()
			calls[id]++
			mu.Unlock()
		}
		return base.RoundTrip(r)
	}))
	saved := func(id, checksum, size int64) cacheproxy.SongURL {
		return cacheproxy.SongURL{SongID: id, URL: fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=%d&saved=1", id, checksum, size)}
	}
	local := CalibrationCandidates{
		SongIDs: []int64{1, 2, 3, 4, 5, 6, 7},
		URLs: []cacheproxy.SongURL{
			saved(1, 1, 16<<20), saved(2, 1, 16<<20), // shared resource counts once
			saved(3, 3, 1<<20), {SongID: 4, URL: "invalid"}, saved(5, 5, 3<<30),
			saved(6, 6, 16<<20), saved(6, 8, 16<<20), // older observation must not replace the first
		},
	}
	c := newTestCalibration(client, "direct", func(context.Context) CalibrationCandidates { return local }, nil)
	defer c.Close()
	c.limits.reserveVideos, c.limits.reserveBytes = 5, 80<<20
	_, samples, err := c.prepare(t.Context())
	state, _ := c.Snapshot()
	if err != nil || len(samples) != 6 || state.PreparedBytes != 81<<20 || catalogs.Load() != 0 {
		t.Fatalf("saved/live reserve: samples=%d bytes=%d catalogs=%d err=%v", len(samples), state.PreparedBytes, catalogs.Load(), err)
	}
	for _, id := range []int64{1, 2, 3, 6} {
		if calls[id] != 0 {
			t.Fatalf("already prepared song %d was resolved again", id)
		}
	}
	seen := map[string]bool{}
	for _, sample := range samples {
		if seen[sample.checksum] || sample.size <= 0 || !strings.HasPrefix(sample.url, "http://") {
			t.Fatal("saved/live samples bypassed shared admission or protocol")
		}
		seen[sample.checksum] = true
	}
	if seen[fmt.Sprintf("%032x", 8)] || !strings.Contains(samples[0].url, "saved=1") {
		t.Fatal("saved address provenance was lost")
	}
}
