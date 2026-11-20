package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestResourceSamplerRotatesSavedAddressesAfterFailure(t *testing.T) {
	c := testConsole(t)
	ctx := t.Context()
	for id, host := range []string{"video.example", "video.example", "video.example", "other.example"} {
		target := fmt.Sprintf("http://%s/files/1/%d-video.mp4?e=%032x&s=1048576", host, id+1, id+1)
		if err := c.store.ObserveSongURL(ctx, cacheproxy.PlaybackObservation{SongID: int64(id + 1), URL: target, Node: "cf"}); err != nil {
			t.Fatal(err)
		}
	}
	var path string
	status := http.StatusForbidden
	c.client.Transport = monitorTestChannels{catalogTransport(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		resp := &http.Response{Request: r, StatusCode: status, Body: http.NoBody}
		if status == http.StatusPartialContent {
			resp.Header = http.Header{"Content-Range": {"bytes 0-1048575/1048576"}}
			resp.ContentLength = 1 << 20
			resp.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 1<<20)))
		}
		return resp, nil
	})}
	cfg := cacheproxy.DefaultConfig()
	c.configureDownloadRoutesLocked(&cfg)
	var rotation resourceSampleRotation
	sample := func(request resourceSampleRequest) error {
		path = ""
		return c.sampleResource(ctx, c.client, cfg, true, &rotation, request)
	}
	for i, code := range []int{http.StatusForbidden, http.StatusNotFound} {
		status = code
		if err := sample(resourceSampleRequest{}); err == nil || path != fmt.Sprintf("/files/1/%d-video.mp4", i+1) {
			t.Fatalf("status %d: path=%q err=%v", code, path, err)
		}
	}
	if rows := cfg.DownloadRoutes.Snapshot(); len(rows) != 0 {
		t.Fatalf("resource errors changed route observations: %v", rows)
	}
	status = http.StatusPartialContent
	manual := resourceSampleRequest{Host: "video.example", IP: "192.0.2.1", reply: make(chan error, 1)}
	if err := sample(manual); err != nil || path != "/files/1/3-video.mp4" {
		t.Fatal("healthy address skipped", path, err)
	}
	rows := cfg.DownloadRoutes.Snapshot()
	if len(rows) != 1 || rows[0].Failed || rows[0].Source != "sample" || rows[0].IP != manual.IP {
		t.Fatal("missing successful block observation", rows)
	}
	status = http.StatusNotFound
	if err := sample(manual); err == nil || path != "/files/1/3-video.mp4" {
		t.Fatal(path, err)
	}
	if latest := cfg.DownloadRoutes.Snapshot(); len(latest) != 1 || latest[0] != rows[0] {
		t.Fatal("404 changed the healthy route observation", latest)
	}
	status = http.StatusPartialContent
	if err := sample(manual); err != nil || path != "/files/1/1-video.mp4" {
		t.Fatal("expected wrap within the requested host", path, err)
	}
}

type savedHostSampleChannels struct{ monitorTestChannels }

func (p savedHostSampleChannels) Current(target string) []upstreamrequest.Candidate {
	candidates := p.monitorTestChannels.Current(target)
	switch candidates[0].Host {
	case "old.example":
		return nil
	case "a.example":
		return candidates[:1]
	default:
		return candidates
	}
}

func (p savedHostSampleChannels) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return p.Current(target), nil
}

func TestAutomaticSamplerVisitsOtherSavedHosts(t *testing.T) {
	c := testConsole(t)
	var contacted string
	c.client.Transport = savedHostSampleChannels{monitorTestChannels{catalogTransport(func(r *http.Request) (*http.Response, error) {
		contacted = r.URL.Hostname()
		return &http.Response{Request: r, StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-0/1"}}, ContentLength: 1, Body: io.NopCloser(strings.NewReader("x"))}, nil
	})}}
	cfg := cacheproxy.DefaultConfig()
	c.configureDownloadRoutesLocked(&cfg)
	hosts := []string{"old.example", "a.example", "b.example", "a.example", "b.example"}
	for i, host := range hosts {
		target := fmt.Sprintf("http://%s/files/1/%d-video.mp4?e=%032x&s=1", host, i+1, i+1)
		if err := c.store.ObserveSongURL(t.Context(), cacheproxy.PlaybackObservation{SongID: int64(i + 1), URL: target, Node: "cf"}); err != nil {
			t.Fatal(err)
		}
	}
	var rotation resourceSampleRotation
	// A has one IP, B has two. The fourth turn finds A cooling down;
	// the fifth can still sample B's second IP.
	for i, want := range []string{"", "a.example", "b.example", "", "b.example", ""} {
		contacted = ""
		err := c.sampleResource(t.Context(), c.client, cfg, true, &rotation, resourceSampleRequest{})
		if contacted != want || (want == "" && !errors.Is(err, cacheproxy.ErrNoSampleDue)) || (want != "" && err != nil) {
			t.Fatalf("turn %d: contacted %q, want %q, err=%v", i+1, contacted, want, err)
		}
	}
	if rows := cfg.DownloadRoutes.Snapshot(); len(rows) != 3 {
		t.Fatalf("expected three sampled host/IP pairs: %v", rows)
	}
}

func TestManualResourceSampleEndpointUsesSharedWorker(t *testing.T) {
	c := testConsole(t)
	const target = "http://video.example/files/123/42-abc.mp4?e=0123456789abcdef0123456789abcdef&s=1"
	var calls atomic.Int32
	c.client.Transport = monitorTestChannels{catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{Request: r, StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-0/1"}}, ContentLength: 1, Body: io.NopCloser(strings.NewReader("x"))}, nil
	})}
	c.resources.ObserveResourceSourcesAtRevision(c.resources.Revision(), "playback", []upstreamrequest.ResourceSource{{ResourceURL: target}}, time.Hour)
	c.startResourceSamplerLocked()
	for i := range 4 {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/upstream/check/throughput", nil)
		want := 403
		if i > 0 {
			want = 200
			r.Header.Set("X-SWD-Token", c.token)
		}
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != want || calls.Load() != int32(i) {
			t.Fatalf("click %d: code=%d calls=%d body=%s", i, w.Code, calls.Load(), w.Body.String())
		}
	}
	rows := c.downloadRoutes.Snapshot()
	if len(rows) != 2 || rows[0].Source != "sample" || rows[1].Source != "sample" {
		t.Fatal(rows)
	}
	// The selected observation must be distinguishable even on a coarse clock.
	for !time.Now().After(rows[1].ObservedAt) {
		time.Sleep(time.Millisecond)
	}
	for _, tc := range []struct {
		body    string
		success bool
	}{
		{`{"host":"video.example","ip":"192.0.2.2"}`, true},
		{`{"host":"video.example","ip":"192.0.2.99"}`, false},
		{`{"host":"other.example","ip":"192.0.2.2"}`, false},
		{`{"ip":"192.0.2.2"}`, false},
	} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/upstream/check/throughput", strings.NewReader(tc.body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if (w.Code == 200) != tc.success || calls.Load() != 4 {
			t.Fatal(tc.body, w.Code, w.Body.String(), calls.Load())
		}
	}
	latest := c.downloadRoutes.Snapshot()
	if latest[0] != rows[0] || latest[1].IP != "192.0.2.2" || !latest[1].ObservedAt.After(rows[1].ObservedAt) {
		t.Fatal(rows, latest)
	}
}

func TestManualSampleCancellationWhileQueuedAndRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var starts, ends atomic.Int32
		tr := catalogTransport(func(r *http.Request) (*http.Response, error) {
			starts.Add(1)
			<-r.Context().Done()
			ends.Add(1)
			return nil, r.Context().Err()
		})
		c := Console{settings: Settings{UpstreamMode: "direct"}, resources: upstreamrequest.NewResources(), downloadRoutes: cacheproxy.NewDownloadRoutes(true, ""), client: &http.Client{Transport: monitorTestChannels{tr}}}
		const target = "http://video.example/files/123/42-abc.mp4?e=0123456789abcdef0123456789abcdef&s=1"
		c.resources.ObserveResourceSourcesAtRevision(c.resources.Revision(), "playback", []upstreamrequest.ResourceSource{{ResourceURL: target}}, time.Hour)
		c.startResourceSamplerLocked()
		defer func() { c.sampleCancel(); <-c.sampleDone }()
		start := func() (context.CancelFunc, <-chan error) {
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- c.requestResourceSample(ctx, resourceSampleRequest{}) }()
			synctest.Wait()
			return cancel, done
		}
		stopRunning, running := start()
		stopQueued, queued := start()
		if starts.Load() != 1 || len(c.sampleRequests) != 1 {
			t.Fatal("request did not queue behind active sample")
		}
		stopQueued()
		if err := <-queued; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		stopRunning()
		if err := <-running; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		synctest.Wait()
		if starts.Load() != 1 || ends.Load() != 1 || len(c.sampleRequests) != 0 || len(c.downloadRoutes.Snapshot()) != 0 {
			t.Fatal("canceled samples left work or observations", starts.Load(), ends.Load(), c.downloadRoutes.Snapshot())
		}
	})
}

// No media engine, download demands or cache are needed to run this worker.
func TestResourceSamplerSharesDirectSlotsOnlyWithItself(t *testing.T) {
	for _, mode := range []string{"direct", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const target = "http://video.example/files/123/42-abc.mp4?e=0123456789abcdef0123456789abcdef&s=1048576"
				var starts, closed atomic.Int32
				tr := catalogTransport(func(r *http.Request) (*http.Response, error) {
					starts.Add(1)
					if r.Header.Get("Range") != "bytes=0-1048575" {
						t.Error(r.Header)
					}
					if mode == "proxy" {
						<-r.Context().Done()
						closed.Add(1)
						return nil, r.Context().Err()
					}
					time.Sleep(time.Second)
					return &http.Response{Request: r, StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-1048575/1048576"}}, ContentLength: 1 << 20, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", 1<<20)))}, nil
				})
				var transport http.RoundTripper = tr
				if mode == "direct" {
					transport = monitorTestChannels{tr}
				}
				c := Console{settings: Settings{UpstreamMode: mode}, resources: upstreamrequest.NewResources(), downloadRoutes: cacheproxy.NewDownloadRoutes(mode == "direct", ""), client: &http.Client{Transport: transport}}
				c.resources.ObserveResourceSourcesAtRevision(c.resources.Revision(), "playback", []upstreamrequest.ResourceSource{{ResourceURL: target}}, time.Hour)
				end := c.resources.BeginResourceLoad()
				c.startResourceSamplerLocked()
				c.startResourceSamplerLocked()
				defer func() { c.sampleCancel(); <-c.sampleDone }()
				if mode == "direct" {
					time.Sleep(32 * time.Second)
				} else {
					time.Sleep(time.Minute)
				}
				synctest.Wait()
				end()
				if mode == "direct" {
					rows := c.downloadRoutes.Snapshot()
					if starts.Load() != 1 || len(rows) != 1 || rows[0].Source != "sample" || rows[0].IP == "" || rows[0].BlockDuration != time.Second {
						t.Fatal(starts.Load(), rows)
					}
					time.Sleep(4 * time.Minute)
					synctest.Wait()
					if starts.Load() != 2 || len(c.downloadRoutes.Snapshot()) != 2 {
						t.Fatal("expected one sample on each IP during the first five minutes", starts.Load(), c.downloadRoutes.Snapshot())
					}
					time.Sleep(time.Minute)
					synctest.Wait()
					if starts.Load() != 3 {
						t.Fatal("expected first IP to become eligible again", starts.Load())
					}
					return
				}
				if starts.Load() != 0 {
					t.Fatal("busy proxy sampled")
				}
				time.Sleep(30 * time.Second)
				synctest.Wait()
				if starts.Load() != 1 {
					t.Fatal("idle proxy was not sampled", starts.Load())
				}
				release := c.resources.BeginResourceLoad()
				synctest.Wait()
				release()
				if closed.Load() != 1 || len(c.downloadRoutes.Snapshot()) != 0 {
					t.Fatal("interrupted proxy sample affected history", closed.Load(), c.downloadRoutes.Snapshot())
				}
			})
		})
	}
}
