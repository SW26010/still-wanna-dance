package upstreamstate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type calibrationCandidates []upstreamrequest.Candidate

func (c calibrationCandidates) RoundTrip(*http.Request) (*http.Response, error) {
	panic("unselected calibration request")
}
func (c calibrationCandidates) Candidates(context.Context, string) ([]upstreamrequest.Candidate, error) {
	return c, nil
}
func (c calibrationCandidates) Current(string) []upstreamrequest.Candidate { return c }
func (c calibrationCandidates) Changed() <-chan struct{}                   { return nil }

func TestCalibrationSelectsOnlyRequestedUpstream(t *testing.T) {
	var direct, proxy atomic.Int32
	paths := calibrationCandidates{
		{Mode: "direct", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			direct.Add(1)
			return response(200, "direct"), nil
		})},
		{Mode: "socks5", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			proxy.Add(1)
			return nil, upstreamrequest.ErrUnavailable
		})},
	}
	channel := upstreamrequest.NewChannel()
	channel.Publish(paths)
	const target = "https://fixture.invalid"
	for _, mode := range []string{"direct", "proxy"} {
		client, err := calibrationClient(context.Background(), channel.Snapshot(), target, mode)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(target)
		if mode == "proxy" && err == nil {
			t.Fatal("failed proxy silently fell back")
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	if direct.Load() != 1 || proxy.Load() != 1 {
		t.Fatal("wrong upstream or fallback")
	}
	channel.Publish(paths[:1])
	if _, err := calibrationClient(context.Background(), channel.Snapshot(), target, "proxy"); err == nil {
		t.Fatal("missing proxy accepted direct candidate")
	}
}

func TestCalibrationRunOwnsUpstreamCleanup(t *testing.T) {
	blocked := transportFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	c := calibrationForTest(t, blocked)
	channel := upstreamrequest.NewChannel()
	channel.Publish(blocked)
	var released atomic.Int32
	if err := c.StartWithUpstream(channel, "proxy", func() { released.Add(1) }); err != nil {
		t.Fatal(err)
	}
	var rejected atomic.Int32
	if err := c.StartWithUpstream(channel, "direct", func() { rejected.Add(1) }); err == nil || rejected.Load() != 1 {
		t.Fatal("rejected run did not release its transport")
	}
	if err := c.StartWithUpstream(channel, "proxy", func() { rejected.Add(1) }); err != nil || rejected.Load() != 2 {
		t.Fatal("duplicate run leaked a transport")
	}
	channel.Release(channel.Snapshot().Revision)
	s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if s.Upstream != "proxy" || s.Phase != "canceled" || released.Load() != 1 {
		t.Fatal("retirement lost upstream or failed to drain before completion")
	}
	c.Close()
	if released.Load() != 1 {
		t.Fatal("double transport cleanup")
	}
}

type hostCalibrationCandidates struct {
	calibrationCandidates
	selectFor func(string) []upstreamrequest.Candidate
}

func (c hostCalibrationCandidates) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return c.selectFor(target), nil
}

type pinnedCalibrationTransport struct {
	host     string
	base     http.RoundTripper
	requests atomic.Int32
	closed   atomic.Int32
}

func (p *pinnedCalibrationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.EqualFold(r.URL.Host, p.host) {
		return nil, upstreamrequest.ErrUnavailable
	}
	p.requests.Add(1)
	return p.base.RoundTrip(r)
}

func (p *pinnedCalibrationTransport) CloseIdleConnections() { p.closed.Add(1) }

func TestCalibrationPinsEachResourceHostAndSharesScheduler(t *testing.T) {
	for _, upstream := range []string{"direct", "proxy"} {
		t.Run(upstream, func(t *testing.T) {
			var active atomic.Int32
			fixture, _ := calibrationManyVideos(t, 8, func(r *http.Request) (*http.Response, error) {
				active.Add(1)
				resp := calibrationRange(r, nil)
				resp.Body = &pacedCalibrationBody{ctx: r.Context(), remaining: resp.ContentLength, active: &active}
				return resp, nil
			})
			base := transportFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := fixture.RoundTrip(r)
				if r.URL.Path == "/Api/Songs/play" && resp != nil {
					var id int
					fmt.Sscanf(r.URL.Query().Get("id"), "%d", &id)
					host := "cf-a.example.com"
					if id > 4 {
						host = "cf-b.example.com"
					}
					location := strings.ReplaceAll(resp.Header.Get("Location"), "play.udon.dance", host)
					resp.Header.Set("Location", strings.ReplaceAll(location, "s=16777216", "s=33554432"))
				}
				return resp, err
			})
			selected := map[string][]*pinnedCalibrationTransport{}
			paths := hostCalibrationCandidates{selectFor: func(target string) []upstreamrequest.Candidate {
				u, err := url.Parse(target)
				if err != nil {
					t.Fatal(err)
				}
				pinned := &pinnedCalibrationTransport{host: u.Host, base: base}
				selected[u.Host] = append(selected[u.Host], pinned)
				mode, otherMode := "direct", "socks5"
				if upstream == "proxy" {
					mode, otherMode = otherMode, mode
				}
				return []upstreamrequest.Candidate{
					{Mode: otherMode, Transport: transportFunc(func(*http.Request) (*http.Response, error) {
						t.Error("used the wrong upstream")
						return nil, upstreamrequest.ErrUnavailable
					})},
					{Mode: mode, Host: u.Host, Transport: pinned},
				}
			}}
			c := calibrationForTest(t, paths)
			c.limits.reserveVideos, c.limits.reserveBytes = 8, 256<<20
			c.limits.warmup, c.limits.window, c.limits.interval = 50*time.Millisecond, 100*time.Millisecond, 10*time.Millisecond
			if err := c.StartWithUpstream(c.channel, upstream, nil); err != nil {
				t.Fatal(err)
			}
			s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
			if s.Reason != "measurement_complete" || s.Phase != "completed" || s.Failures != 0 || s.PeakActive != 16 || s.PeakOpen != 16 || active.Load() != 0 {
				t.Fatalf("multi-host measurement failed or bypassed shared scheduler: %+v active=%d", s, active.Load())
			}
			for _, host := range []string{"cf-a.example.com", "cf-b.example.com"} {
				pins := selected[host]
				if len(pins) != 1 || pins[0].requests.Load() == 0 || pins[0].closed.Load() != 1 {
					t.Errorf("host %s was not pinned, used and closed exactly once: %+v", host, pins)
				}
			}
		})
	}
}
