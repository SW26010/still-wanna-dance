package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/upstreamrequest"
)

type routeFixture []upstreamrequest.Candidate

func (f routeFixture) Current(string) []upstreamrequest.Candidate { return f }
func (f routeFixture) Candidates(context.Context, string) ([]upstreamrequest.Candidate, error) {
	return f, nil
}
func (routeFixture) Changed() <-chan struct{} { return nil }

func readFixtureBlock(t *testing.T, s *Server, target string, needBy time.Time) error {
	t.Helper()
	r, err := http.NewRequest("GET", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	w := testResourceSession(t, s, v)
	w.f.ranges.video = v
	w.f.discard, w.f.log = true, s.cfg.Logger
	w.f.progress = &downloadProgress{log: w.f.log, start: time.Now()}
	return s.readRange(t.Context(), w.f, 0, func() {}, func() time.Time { return needBy })
}

func TestSharedRoutesBodyTimeoutAcrossDownloads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		s.cfg = DefaultConfig()
		s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
		const host = "play.udon.dance"
		data := strings.Repeat("a", int(DefaultBlockSize))
		var calls []string
		var cs routeFixture
		for i, ip := range []string{"2001:db8::1", "192.0.2.1"} {
			tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, ip)
				resp := fixtureRangeResponse(r, data)
				// Both paths return headers immediately. Only the last IP may spend 35s on its body.
				delay := 35 * time.Second
				if i == 1 && len(calls) > 2 {
					delay = time.Second
				}
				resp.Body = &delayedResourceBody{ReadCloser: resp.Body, ctx: r.Context(), delay: delay}
				return resp, nil
			})
			cs = append(cs, upstreamrequest.Candidate{Mode: "direct", Host: host, IP: ip, Transport: tr})
		}
		s.cfg.DirectCandidates = cs
		for _, id := range []string{"1344", "1345"} {
			if err := readFixtureBlock(t, s, strings.Replace(videoURL(data), "1344-", id+"-", 1), time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(calls, []string{"2001:db8::1", "192.0.2.1", "192.0.2.1"}) {
			t.Fatal(calls)
		}
		rows := s.cfg.DownloadRoutes.Snapshot()
		if len(rows) != 2 || rows[0].IP != "192.0.2.1" || rows[0].BlockDuration != time.Second || !rows[1].Failed {
			t.Fatal(rows)
		}
	})
}

func TestConcurrentBlocksReportCurrentRouteThroughRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 2})
		s.cfg = DefaultConfig()
		s.cfg.BlockSize = 4
		s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
		const data = "12345678"
		req, _ := http.NewRequest("GET", videoURL(data), nil)
		v, err := s.parse(req)
		if err != nil {
			t.Fatal(err)
		}
		w := testResourceSession(t, s, v)
		w.f.ranges.video, w.f.discard, w.f.log = v, true, s.cfg.Logger
		w.f.progress = &downloadProgress{log: w.f.log, start: time.Now()}
		retry, finish := make(chan struct{}), make(chan struct{})
		var paths routeFixture
		for i, ip := range []string{"2001:db8::1", "192.0.2.1"} {
			tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				if i == 0 {
					<-retry
					return nil, io.ErrUnexpectedEOF
				}
				<-finish
				return fixtureRangeResponse(r, data), nil
			})
			paths = append(paths, upstreamrequest.Candidate{Host: v.host, Mode: "direct", IP: ip, Transport: tr})
		}
		s.cfg.DirectCandidates = paths
		results := make(chan error, 2)
		for block := range int64(2) {
			go func() {
				results <- s.readRange(t.Context(), w.f, block, func() {}, func() time.Time { return time.Time{} })
			}()
			synctest.Wait()
		}
		observer := blockObserver{s: s}
		check := func(first, second string) {
			t.Helper()
			ips := observer.captureInput(time.Now()).upstreamIPs
			if ips[v.key+"/0"] != first || ips[v.key+"/1"] != second {
				t.Fatalf("block routes = %v, want %q and %q", ips, first, second)
			}
		}
		check(paths[0].IP, paths[1].IP)
		close(retry)
		synctest.Wait()
		check(paths[1].IP, paths[1].IP)
		close(finish)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		check("", "")
	})
}

func TestHeaderBudgetKeepsOtherRoutesHealthy(t *testing.T) {
	for _, expireBeforeRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(expireBeforeRetry), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
				s.cfg = DefaultConfig()
				s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
				var paths routeFixture
				var calls []int
				for i := range 3 {
					tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls = append(calls, i)
						resp := fixtureRangeResponse(r, payload)
						if i == 0 {
							if !expireBeforeRetry {
								<-r.Context().Done()
								return nil, r.Context().Err()
							}
							resp.Body = &delayedResourceBody{ReadCloser: io.NopCloser(strings.NewReader("x")), ctx: r.Context(), delay: 2 * time.Second}
						} else {
							select {
							case <-time.After(time.Millisecond):
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
						}
						return resp, nil
					})
					paths = append(paths, upstreamrequest.Candidate{Host: "play.udon.dance", Mode: "direct", IP: fmt.Sprintf("192.0.2.%d", i+1), Transport: tr})
				}
				s.cfg.DirectCandidates = paths
				err := readFixtureBlock(t, s, videoURL(payload), time.Now().Add(time.Second-sourceGrace))
				rows := s.cfg.DownloadRoutes.Snapshot()
				if err != nil || !slices.Equal(calls, []int{0, 1}) || len(rows) == 0 || rows[0].IP != paths[1].IP || rows[0].Failed {
					t.Fatal(err, calls, rows)
				}
				if expireBeforeRetry {
					if len(rows) != 2 || !rows[1].Failed {
						t.Fatal("short read missing", rows)
					}
				} else if len(rows) != 1 {
					t.Fatal("local header budget affected route", rows)
				}
			})
		})
	}
}

func TestInvalidRedirectKeepsIPHealthy(t *testing.T) {
	cfg := DefaultConfig()
	h := NewDownloadRoutes(true, "")
	cfg.DownloadRoutes = h
	key := routeKey{"play.udon.dance", "192.0.2.2"}
	h.record(key, "download", DefaultBlockSize, false, time.Second)
	before := h.Snapshot()[0]
	calls := 0
	tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/%zz"}}, Body: http.NoBody, Request: r}, nil
	})
	cfg.DirectCandidates = routeFixture{{Host: key.host, Mode: "direct", IP: key.ip, Transport: tr}}
	err := SampleBlockOnIP(t.Context(), cfg, &http.Client{}, videoURL(payload), 0, key.ip)
	if err == nil || calls != 1 || h.Snapshot()[0] != before {
		t.Fatal(err, calls, h.Snapshot())
	}
}

func TestTargetedSampleNeverSubstitutesAnotherRoute(t *testing.T) {
	const host, otherHost = "play.udon.dance", "other.example"
	cfg := DefaultConfig()
	cfg.DownloadRoutes = NewDownloadRoutes(true, "")
	cfg.IsResourceHost = func(h string) bool { return h == host || h == otherHost }
	ip := "2001:db8::8"
	calls := 0
	redirect := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		next := *r.URL
		next.Host = otherHost
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {next.String()}}, Body: http.NoBody, Request: r}, nil
	})
	unexpected := resourceTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("targeted sample used a different route")
		return nil, errors.New("unexpected transport")
	})
	paths := routeFixture{
		{Host: host, Mode: "direct", IP: "192.0.2.1", Transport: unexpected},
		{Host: host, Mode: "direct", IP: ip, Transport: redirect},
		{Host: otherHost, Mode: "direct", IP: ip, Transport: unexpected},
	}
	cfg.DirectCandidates = paths
	sample := func() error {
		return SampleBlockOnIP(t.Context(), cfg, &http.Client{Transport: unexpected}, videoURL(payload), 0, ip)
	}
	cfg.DownloadRoutes.choose(host, paths[1:2], nil, time.Time{}, nil)
	err := sample()
	cfg.DownloadRoutes.release(routeKey{host, ip})
	if !errors.Is(err, ErrNoSampleDue) || calls != 0 {
		t.Fatal(err, calls)
	}
	if err := sample(); err == nil || calls != 1 || len(cfg.DownloadRoutes.Snapshot()) != 0 {
		t.Fatal(err, calls, cfg.DownloadRoutes.Snapshot())
	}
}

func TestRangeRetryWithExpiredLeases(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		fixed                   int
		expire, shortFallback   bool
		wantFixed, wantOrdinary int
		wantOK                  bool
	}{
		{"fallback-recovers", 1, true, false, 1, 1, true},
		{"fallback-only-once", 1, true, true, 1, 1, false},
		{"still-valid-and-tried", 2, false, false, 2, 0, false},
		{"budget-exhausted", upstreamrequest.MaxOperationCandidates, true, false, upstreamrequest.MaxOperationCandidates, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
				s.cfg = DefaultConfig()
				s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
				var fixed, ordinary int
				var cs routeFixture
				for i := range tc.fixed {
					tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						fixed++
						resp := fixtureRangeResponse(r, payload)
						resp.Body = io.NopCloser(strings.NewReader(payload[:1]))
						if tc.expire && fixed == tc.fixed {
							time.Sleep(time.Second)
						}
						return resp, nil
					})
					cs = append(cs, upstreamrequest.Candidate{Host: "play.udon.dance", Mode: "direct", IP: fmt.Sprintf("192.0.2.%d", i+1), Transport: tr, ValidUntil: time.Now().Add(time.Second)})
				}
				s.cfg.DirectCandidates = cs
				s.client.Transport = resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					ordinary++
					resp := fixtureRangeResponse(r, payload)
					if tc.shortFallback {
						resp.Body = io.NopCloser(strings.NewReader(payload[:1]))
					}
					return resp, nil
				})
				err := readFixtureBlock(t, s, videoURL(payload), time.Time{})
				if (err == nil) != tc.wantOK || fixed != tc.wantFixed || ordinary != tc.wantOrdinary {
					t.Fatalf("fixed=%d ordinary=%d err=%v", fixed, ordinary, err)
				}
			})
		})
	}
}

type coldRouteFixture struct {
	routeFixture
	discover func(context.Context) ([]upstreamrequest.Candidate, error)
}

func (f coldRouteFixture) Candidates(ctx context.Context, _ string) ([]upstreamrequest.Candidate, error) {
	return f.discover(ctx)
}

func TestColdRangeDiscoversBeforeUsingRestoredHistory(t *testing.T) {
	const host = "play.udon.dance"
	const fast = "192.0.2.2"
	path := filepath.Join(t.TempDir(), "routes.json")
	h := NewDownloadRoutes(true, path)
	h.record(routeKey{host, fast}, "download", DefaultBlockSize, false, time.Second)
	h.Flush()
	s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
	s.cfg = DefaultConfig()
	s.cfg.DownloadRoutes = NewDownloadRoutes(true, path)
	var calls []string
	var cs routeFixture
	for _, ip := range []string{"2001:db8::1", fast} {
		tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, ip)
			return fixtureRangeResponse(r, payload), nil
		})
		cs = append(cs, upstreamrequest.Candidate{Host: host, Mode: "direct", IP: ip, Transport: tr})
	}
	s.cfg.DirectCandidates = coldRouteFixture{discover: func(context.Context) ([]upstreamrequest.Candidate, error) {
		calls = append(calls, "dns")
		return cs, nil
	}}
	s.client.Transport = resourceTransportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected ordinary dial") })
	if err := readFixtureBlock(t, s, videoURL(payload), time.Now()); err != nil || !slices.Equal(calls, []string{"dns", fast}) {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}

func TestColdDiscoveryLeavesTimeForOrdinaryDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		s.cfg = DefaultConfig()
		s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
		s.cfg.DownloadTimeout = 400 * time.Millisecond
		var discoveries int
		s.cfg.DirectCandidates = coldRouteFixture{discover: func(ctx context.Context) ([]upstreamrequest.Candidate, error) {
			discoveries++
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		start := time.Now()
		s.client.Transport = resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			return fixtureRangeResponse(r, payload), nil
		})
		if err := readFixtureBlock(t, s, videoURL(payload), time.Now()); err != nil || discoveries != 1 || time.Since(start) != 200*time.Millisecond {
			t.Fatalf("discoveries=%d elapsed=%v err=%v", discoveries, time.Since(start), err)
		}
	})
}

func TestUrgentUnmeasuredRoutesDistributeWithinDNSBaseline(t *testing.T) {
	h := NewDownloadRoutes(true, "")
	const host = "video.example"
	var cs routeFixture
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		cs = append(cs, upstreamrequest.Candidate{Host: host, IP: ip, Mode: "direct", Transport: http.DefaultTransport, ValidUntil: time.Now().Add(time.Minute)})
	}
	first, _, _ := h.choose(host, cs, nil, time.Now(), nil)
	h.release(routeKey{host, first.IP})
	for i := range 2 {
		cs[i].ValidUntil = cs[i].ValidUntil.Add(time.Minute)
	}
	for _, want := range []string{cs[0].IP, cs[1].IP, cs[0].IP} {
		got, _, _ := h.choose(host, cs, nil, time.Now(), nil)
		defer h.release(routeKey{host, got.IP})
		if got.IP != want {
			t.Fatalf("got %s want %s", got.IP, want)
		}
	}
}

func TestUrgentRoutesSpreadOnlyNearFastestBlock(t *testing.T) {
	const host = "video.example"
	const fast, closeIP, slow = "192.0.2.1", "192.0.2.2", "192.0.2.3"
	for _, order := range [][]string{{fast, closeIP, slow}, {slow, closeIP, fast}} {
		h := NewDownloadRoutes(true, "")
		var cs routeFixture
		for _, ip := range order {
			cs = append(cs, upstreamrequest.Candidate{Host: host, IP: ip, Mode: "direct", Transport: http.DefaultTransport})
		}
		for ip, duration := range map[string]time.Duration{fast: time.Second, closeIP: 1100 * time.Millisecond, slow: 1200 * time.Millisecond} {
			h.record(routeKey{host, ip}, "download", DefaultBlockSize, false, duration)
		}
		pick := func(want string, slack time.Duration, tried map[string]bool) {
			t.Helper()
			got, _, _ := h.choose(host, cs, tried, time.Now().Add(slack), nil)
			if got.IP != want {
				t.Fatalf("order %v, slack %v, tried %v: got %s, want %s", order, slack, tried, got.IP, want)
			}
			t.Cleanup(func() { h.release(routeKey{host, got.IP}) })
		}
		// Keep every reservation active, as when several blocks start together.
		for _, want := range []string{fast, closeIP, fast, closeIP, fast} {
			pick(want, 20*time.Second, nil)
		}
		pick(fast, 1050*time.Millisecond, nil)                  // Only the fastest fits.
		pick(fast, 500*time.Millisecond, nil)                   // None fit.
		pick(slow, 20*time.Second, map[string]bool{fast: true}) // Compare against the fastest remaining retry.
	}
}

func TestSampleRedirectDeadlineAttribution(t *testing.T) {
	for _, failure := range []string{"body deadline", "header deadline", "short body"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.DownloadRoutes = NewDownloadRoutes(true, "")
				const target = "video.example"
				cfg.IsResourceHost = func(h string) bool { return h == target }
				final := routeKey{target, "192.0.2.2"}
				cfg.DownloadRoutes.record(final, "download", DefaultBlockSize, false, 2*time.Second)
				time.Sleep(sampleInterval)
				origin := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					time.Sleep(9 * time.Second)
					u := *r.URL
					u.Host = target
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {u.String()}}, Body: http.NoBody, Request: r}, nil
				})
				last := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					if failure == "header deadline" {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					resp := fixtureRangeResponse(r, payload)
					if failure == "short body" {
						resp.Body = io.NopCloser(strings.NewReader(payload[:1]))
					} else {
						resp.Body = &delayedResourceBody{ReadCloser: resp.Body, ctx: r.Context(), delay: 2 * time.Second}
					}
					return resp, nil
				})
				cfg.DirectCandidates = routeFixture{
					{Host: "play.udon.dance", Mode: "direct", IP: "192.0.2.1", Transport: origin},
					{Host: target, Mode: "direct", IP: final.ip, Transport: last},
				}
				start := time.Now()
				err := SampleBlock(t.Context(), cfg, &http.Client{}, videoURL(payload), 0, false)
				rows := cfg.DownloadRoutes.Snapshot()
				wantErr := error(context.DeadlineExceeded)
				if failure == "short body" {
					wantErr = io.ErrUnexpectedEOF
				}
				if !errors.Is(err, wantErr) || len(rows) != 1 || rows[0].IP != final.ip || rows[0].Failed != (failure == "short body") {
					t.Fatalf("err=%v rows=%v", err, rows)
				}
				if failure != "short body" && (time.Since(start) != 10*time.Second || rows[0].Source != "download") {
					t.Fatalf("elapsed=%v row=%+v", time.Since(start), rows[0])
				}
			})
		})
	}
}

func TestSampleTimeoutOnSingleKnownIP(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.DownloadRoutes = NewDownloadRoutes(true, "")
		tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		cfg.DirectCandidates = routeFixture{{Host: "play.udon.dance", Mode: "direct", IP: "192.0.2.1", Transport: tr}}
		err := SampleBlock(t.Context(), cfg, &http.Client{}, videoURL(payload), 0, false)
		rows := cfg.DownloadRoutes.Snapshot()
		if !errors.Is(err, context.DeadlineExceeded) || len(rows) != 1 || !rows[0].Failed {
			t.Fatalf("err=%v rows=%v", err, rows)
		}
	})
}

func TestProxySampleIntervalIncludesDownloadsAcrossHosts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.DownloadRoutes = NewDownloadRoutes(false, "")
		var requests int
		tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			return fixtureRangeResponse(r, payload), nil
		})
		sample := func() error {
			return SampleBlock(t.Context(), cfg, &http.Client{Transport: tr}, videoURL(payload), 0, false)
		}
		if err := sample(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(4 * time.Minute)
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		s.cfg, s.client.Transport = cfg, tr
		other := strings.Replace(videoURL(payload), "play.udon.dance", "video.example", 1)
		if err := readFixtureBlock(t, s, other, time.Time{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(sampleInterval - time.Second)
		if err := sample(); !errors.Is(err, ErrNoSampleDue) || requests != 2 {
			t.Fatalf("early sample: requests=%d err=%v", requests, err)
		}
		time.Sleep(time.Second)
		if err := sample(); err != nil || requests != 3 {
			t.Fatalf("due sample: requests=%d err=%v", requests, err)
		}
	})
}

func TestDirectSamplingNeedsIdentifiedIPAndAllowsSameRouteRedirect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DownloadRoutes = NewDownloadRoutes(true, "")
	var requests int
	tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			u := *r.URL
			u.RawQuery += "&next=1"
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {u.String()}}, Body: http.NoBody, Request: r}, nil
		}
		return fixtureRangeResponse(r, payload), nil
	})
	cfg.DirectCandidates = routeFixture{}
	sample := func() error {
		return SampleBlock(t.Context(), cfg, &http.Client{Transport: tr}, videoURL(payload), 0, false)
	}
	if err := sample(); !errors.Is(err, ErrNoSampleDue) || requests != 0 {
		t.Fatalf("unidentified route: requests=%d err=%v", requests, err)
	}
	cfg.DirectCandidates = routeFixture{{Host: "play.udon.dance", Mode: "direct", IP: "192.0.2.1", Transport: tr}}
	if err := sample(); err != nil || requests != 2 {
		t.Fatalf("redirect: requests=%d err=%v", requests, err)
	}
	if err := sample(); !errors.Is(err, ErrNoSampleDue) || requests != 2 {
		t.Fatalf("same route sampled again: requests=%d err=%v", requests, err)
	}
}

func TestSampleRedirectRechecksInFlight(t *testing.T) {
	const host = "play.udon.dance"
	cfg := DefaultConfig()
	h := NewDownloadRoutes(true, "")
	cfg.DownloadRoutes = h
	requests := 0
	tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests > 1 {
			return fixtureRangeResponse(r, payload), nil
		}
		u := *r.URL
		u.RawQuery += "&next=1"
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {u.String()}}, Body: http.NoBody, Request: r}, nil
	})
	paths := routeFixture{{Host: host, Mode: "direct", IP: "192.0.2.1", Transport: tr}}
	cfg.DirectCandidates = paths
	// A business download takes the released path before the next hop.
	cfg.BeginResourceLoad = func() func() {
		return func() {
			if requests == 1 {
				selected, n, _ := h.choose(host, paths, nil, time.Time{}, nil)
				if n != 1 {
					t.Fatal("business route unavailable")
				}
				t.Cleanup(func() { h.release(routeKey{host, selected.IP}) })
			}
		}
	}
	err := SampleBlock(t.Context(), cfg, &http.Client{Transport: tr}, videoURL(payload), 0, false)
	if !errors.Is(err, ErrNoSampleDue) || requests != 1 {
		t.Fatalf("redirect on busy IP: requests=%d err=%v", requests, err)
	}
}

type fallbackTestConn struct {
	net.Conn
	addr *net.TCPAddr
}

func (c fallbackTestConn) RemoteAddr() net.Addr { return c.addr }

func TestFallbackPeerReservationsFollowConnection(t *testing.T) {
	const host = "play.udon.dance"
	h := NewDownloadRoutes(true, "")
	a := networkAttempt{history: h, parent: t.Context(), key: routeKey{host: host}, started: time.Now()}
	trace := httptrace.ContextClientTrace(a.trace(t.Context(), func() {}))
	connect := func(ip string) {
		trace.GotConn(httptrace.GotConnInfo{Conn: fallbackTestConn{addr: &net.TCPAddr{IP: net.ParseIP(ip)}}})
	}
	check := func(ip string, idle bool) {
		t.Helper()
		paths := routeFixture{{Host: host, Mode: "direct", IP: ip, Transport: http.DefaultTransport}}
		_, n, _ := h.choose(host, paths, nil, time.Time{}, &blockSample{manual: true, visited: make(map[routeKey]bool)})
		if (n == 1) != idle {
			t.Fatalf("peer %s: sample candidates=%d, want idle=%v", ip, n, idle)
		}
		if n == 1 {
			h.release(routeKey{host, ip})
		}
	}
	const first, next = "2001:db8::1", "192.0.2.1"
	connect(first)
	connect(first)
	check(first, false)
	trace.GetConn(host)
	check(first, true)
	connect(first)
	connect(next)
	check(first, true)
	check(next, false)
	a.release()
	connect(first) // A late callback cannot reacquire or change retry attribution.
	check(first, true)
	check(next, true)
	if a.currentIP() != next {
		t.Fatal("lost peer for next retry", a.currentIP())
	}
}

func TestSampleFailureSteersPrefetchAndCachedBlockIsStillSampled(t *testing.T) {
	const host = "play.udon.dance"
	var attempts []string
	routes := NewDownloadRoutes(true, "")
	var paths routeFixture
	for _, ip := range []string{"2001:db8::1", "192.0.2.1"} {
		tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			attempts = append(attempts, ip)
			resp := fixtureRangeResponse(r, payload)
			if ip == "2001:db8::1" {
				resp.Body = io.NopCloser(strings.NewReader(payload[:1]))
			}
			return resp, nil
		})
		paths = append(paths, upstreamrequest.Candidate{Host: host, Mode: "direct", IP: ip, Transport: tr})
	}
	s, cfg := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected unpinned request") }, func(cfg *Config) {
		cfg.DownloadRoutes, cfg.DirectCandidates = routes, paths
		cfg.BlockSize = DefaultBlockSize
	})
	target := videoURL(payload)
	if err := SampleBlock(t.Context(), cfg, s.client, target, 0, false); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if _, err := s.Prefetch(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prefetch(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := SampleBlock(t.Context(), cfg, s.client, target, 0, false); !errors.Is(err, ErrNoSampleDue) {
		t.Fatal("recent failed sample and successful download should both suppress sampling", err)
	}
	// The file is still cached when DNS gains a third, previously unseen IP.
	cfg.DirectCandidates = append(paths, upstreamrequest.Candidate{Host: host, Mode: "direct", IP: "192.0.2.2", Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		attempts = append(attempts, "192.0.2.2")
		return fixtureRangeResponse(r, payload), nil
	})})
	if err := SampleBlock(t.Context(), cfg, s.client, target, 0, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(attempts, []string{"2001:db8::1", "192.0.2.1", "192.0.2.2"}) {
		t.Fatal(attempts)
	}
	rows := routes.Snapshot()
	if len(rows) != 3 || rows[1].Source != "sample" || !rows[2].Failed {
		t.Fatal(rows)
	}
}

func TestRouteSamplingAndRestart(t *testing.T) {
	data := strings.Repeat("x", 2*int(DefaultBlockSize))
	cfg := DefaultConfig()
	path := filepath.Join(t.TempDir(), "download-routes.json")
	cfg.DownloadRoutes = NewDownloadRoutes(true, path)
	tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		time.Sleep(2 * time.Millisecond)
		if r.Header.Get("Range") != "bytes=1048576-2097151" {
			t.Fatal(r.Header)
		}
		return fixtureRangeResponse(r, data), nil
	})
	cs := routeFixture{{Mode: "direct", Host: "play.udon.dance", IP: "192.0.2.3", Transport: tr}}
	cfg.DirectCandidates = cs
	if err := SampleBlock(t.Context(), cfg, &http.Client{}, videoURL(data), 1, false); err != nil {
		t.Fatal(err)
	}
	observed := cfg.DownloadRoutes.Snapshot()[0]
	if observed.Source != "sample" || observed.BlockDuration <= 0 {
		t.Fatal(observed)
	}
	cfg.DownloadRoutes.record(routeKey{observed.Host, observed.IP}, "download", 0, true, time.Second)
	cfg.DownloadRoutes.Flush()
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "files/") {
		t.Fatal(string(raw), err)
	}
	restored := NewDownloadRoutes(true, path)
	rows := restored.Snapshot()
	if len(rows) != 1 || rows[0].Failed || !rows[0].ObservedAt.Equal(observed.ObservedAt) || rows[0].BlockDuration != observed.BlockDuration {
		t.Fatal(rows)
	}
	cfg.DownloadRoutes = restored
	if err := SampleBlock(t.Context(), cfg, &http.Client{}, videoURL(data), 1, false); !errors.Is(err, ErrNoSampleDue) {
		t.Fatal("restart should honor a recent successful observation", err)
	}
}

func TestRestoredObservationsKeepAgeAndDNSMembership(t *testing.T) {
	const host = "video.example"
	path := filepath.Join(t.TempDir(), "routes.json")
	h := NewDownloadRoutes(true, path)
	hourAgo := time.Now().Add(-time.Hour).UTC()
	for i, at := range []time.Time{hourAgo, hourAgo.Add(-24 * time.Hour)} {
		ip := []string{"192.0.2.1", "192.0.2.2"}[i]
		h.record(routeKey{host, ip}, "download", DefaultBlockSize, false, time.Second)
		h.mu.Lock()
		entry := h.entries[routeKey{host, ip}]
		entry.ObservedAt, entry.successAt, entry.BlockAt = at, at, at
		h.mu.Unlock()
	}
	h.Flush()
	restored := NewDownloadRoutes(true, path)
	rows := restored.Snapshot()
	if len(rows) != 1 || !rows[0].ObservedAt.Equal(hourAgo) {
		t.Fatal(rows)
	}
	cs := routeFixture{{Host: host, Mode: "direct", IP: "2001:db8::1", Transport: http.DefaultTransport}}
	chosen, n, _ := restored.choose(host, cs, nil, time.Now(), nil)
	if n != 1 || chosen.IP != cs[0].IP {
		t.Fatal("restored IP became DNS candidate", chosen)
	}
}

func TestFallbackObservationRequiresConfirmedPeer(t *testing.T) {
	runDownload := func(cfg Config, tr http.RoundTripper) error {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		s.cfg, s.client.Transport = cfg, tr
		return readFixtureBlock(t, s, videoURL(payload), time.Time{})
	}
	for _, direct := range []bool{true, false} {
		cfg := DefaultConfig()
		cfg.DownloadRoutes = NewDownloadRoutes(direct, "")
		tr := resourceTransportFunc(func(r *http.Request) (*http.Response, error) { return fixtureRangeResponse(r, payload), nil })
		if err := runDownload(cfg, tr); err != nil {
			t.Fatal(err)
		}
		rows := cfg.DownloadRoutes.Snapshot()
		if direct && len(rows) != 0 || !direct && (len(rows) != 1 || rows[0].IP != "" || rows[0].BlockDuration != 0) {
			t.Fatal(rows)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}}
	defer tr.CloseIdleConnections()
	cfg := DefaultConfig()
	cfg.DownloadRoutes = NewDownloadRoutes(true, "")
	if err := runDownload(cfg, tr); err != nil {
		t.Fatal(err)
	}
	if rows := cfg.DownloadRoutes.Snapshot(); len(rows) != 1 || rows[0].IP != u.Hostname() {
		t.Fatal(rows)
	}
}

func TestRouteChoiceUsesNeedAndActiveAttempts(t *testing.T) {
	h := NewDownloadRoutes(true, "")
	const host = "video.example"
	cs := routeFixture{{Mode: "direct", Host: host, IP: "192.0.2.1", Transport: http.DefaultTransport}, {Mode: "direct", Host: host, IP: "2001:db8::1", Transport: http.DefaultTransport}}
	choice, _, _ := h.choose(host, cs, nil, time.Time{}, nil)
	next, _, _ := h.choose(host, cs, nil, time.Time{}, nil)
	if choice.IP == next.IP {
		t.Fatal("concurrent cold attempts selected the same IP")
	}
	h.record(routeKey{host, choice.IP}, "download", DefaultBlockSize, false, time.Second)
	h.record(routeKey{host, next.IP}, "download", 0, false, 0)
	h.release(routeKey{host, choice.IP})
	h.release(routeKey{host, next.IP})
	urgent, _, _ := h.choose(host, cs, nil, time.Now(), nil)
	explore, _, _ := h.choose(host, cs, nil, time.Time{}, nil)
	if urgent.IP != choice.IP || explore.IP != next.IP {
		t.Fatal(urgent, explore)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	a := networkAttempt{history: h, key: routeKey{host, urgent.IP}, reserved: true, parent: canceled, started: time.Now()}
	a.finish(0, io.ErrUnexpectedEOF)
	a.release()
	a = networkAttempt{history: h, key: routeKey{host, explore.IP}, reserved: true, parent: t.Context(), started: time.Now()}
	a.finish(0, errors.Join(upstreamrequest.ErrUnavailable, errors.New("expired")))
	a.release()
	if rows := h.Snapshot(); len(rows) != 1 || rows[0].Failed {
		t.Fatal(rows)
	}
}
