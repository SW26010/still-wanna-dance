package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type stalledRangeBody struct {
	ctx     context.Context
	entered func()
	ended   chan<- error
}

func (b stalledRangeBody) Read([]byte) (int, error) {
	b.entered()
	<-b.ctx.Done()
	b.ended <- b.ctx.Err()
	return 0, b.ctx.Err()
}

func (stalledRangeBody) Close() error { return nil }

func waitForSharedAddress(ctx context.Context, s *Server, key, target string) error {
	s.mu.Lock()
	a := &s.flights[key].addresses
	s.mu.Unlock()
	for {
		a.mu.Lock()
		changed := a.changed
		published := slices.ContainsFunc(a.urls, func(v video) bool { return v.requestURL() == target })
		a.mu.Unlock()
		if published {
			return ctx.Err()
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestAddressDiscoveryUsesNextAttemptAfterTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			stalled := make(chan struct{})
			ended := make(chan error, 1)
			enter := sync.OnceFunc(func() { close(stalled) })
			old := videoURL(payload)
			fresh := old + "&address=fresh"
			s, _ := setup(t, nil, func(cfg *Config) {
				cfg.DownloadTimeout = 500 * time.Millisecond
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					select {
					case <-stalled:
						return fresh, nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
			})
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: old}); err != nil {
				t.Fatal(err)
			}
			var recovered atomic.Int32
			s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
				if target == fresh {
					return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						recovered.Add(1)
						return fixtureRangeResponse(r, payload), nil
					})}
				}
				return []http.RoundTripper{
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Request: r}, nil
					}),
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						if phase == "body" {
							resp := fixtureRangeResponse(r, payload)
							resp.Body = stalledRangeBody{r.Context(), enter, ended}
							return resp, nil
						}
						enter()
						<-r.Context().Done()
						ended <- r.Context().Err()
						return nil, r.Context().Err()
					}),
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			resp := httptest.NewRecorder()
			s.ServeHTTP(resp, httptest.NewRequestWithContext(ctx, http.MethodGet, old, nil))
			if ctx.Err() != nil || resp.Code != 200 || resp.Body.String() != payload || recovered.Load() != 1 {
				t.Fatalf("status=%d body=%q fresh=%d error=%v", resp.Code, resp.Body.String(), recovered.Load(), ctx.Err())
			}
			if err := <-ended; !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("old path ended before its timeout: %v", err)
			}
		})
	}
}

func TestNewAddressWaitsForCurrentBody(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprintf("short=%v", short), func(t *testing.T) {
			old := videoURL(payload)
			fresh := old + "&address=fresh"
			bodyStarted := make(chan struct{})
			s, _ := setup(t, nil, func(cfg *Config) {
				cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
					select {
					case <-bodyStarted:
						return fresh, nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
			})
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: old}); err != nil {
				t.Fatal(err)
			}
			var freshCalls, thirdIP atomic.Int32
			s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
				if target == fresh {
					return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						freshCalls.Add(1)
						return fixtureRangeResponse(r, payload), nil
					})}
				}
				return []http.RoundTripper{
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 403, Body: http.NoBody, Request: r}, nil
					}),
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						resp := fixtureRangeResponse(r, payload)
						tail := publishReaderFunc(func(p []byte) (int, error) {
							close(bodyStarted)
							// Deliver the last byte only after discovery has published the alternative.
							if err := waitForSharedAddress(r.Context(), s, videoKey(payload), fresh); err != nil {
								return 0, err
							}
							if short {
								return 0, io.ErrUnexpectedEOF
							}
							p[0] = payload[len(payload)-1]
							return 1, io.EOF
						})
						resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(payload[:len(payload)-1]), tail))
						return resp, nil
					}),
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						thirdIP.Add(1)
						return nil, errors.New("unexpected old URL retry")
					}),
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			resp := httptest.NewRecorder()
			s.ServeHTTP(resp, httptest.NewRequestWithContext(ctx, http.MethodGet, old, nil))
			wantFresh := int32(0)
			if short {
				wantFresh = 1
			}
			if resp.Code != 200 || resp.Body.String() != payload || freshCalls.Load() != wantFresh || thirdIP.Load() != 0 {
				t.Fatalf("status=%d body=%q fresh=%d third IP=%d", resp.Code, resp.Body.String(), freshCalls.Load(), thirdIP.Load())
			}
		})
	}
}

func TestFillWaitsForCoolingIP(t *testing.T) {
	s, _ := setup(t, nil, func(cfg *Config) { cfg.CacheUnlimited = true })
	target := seedPreview(t, s, payload)
	if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(payload)}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	path := upstreamrequest.Candidate{Mode: "direct", Host: "play.udon.dance", IP: "192.0.2.1",
		Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return fixtureRangeResponse(r, payload), nil
		})}
	s.cfg.DirectCandidates = routeFixture{path}
	s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
	s.cfg.DownloadRoutes.record(routeKey{path.Host, path.IP}, "download", 0, true, time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.PrefetchDownload(withExpectedMD5(ctx, target.Resource), 1); err != nil || calls.Load() != 1 {
		t.Fatalf("media requests=%d err=%v", calls.Load(), err)
	}
}

func TestAddresslessBlockUsesImportedFile(t *testing.T) {
	lookup := make(chan struct{})
	started := sync.OnceFunc(func() { close(lookup) })
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected media request") }, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(ctx context.Context, _ string) (string, error) {
			started()
			<-ctx.Done()
			return "", ctx.Err()
		}
	})
	target := seedPreview(t, s, payload)
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan *responseRecorder, 1)
	go func() { done <- previewResponseContext(ctx, s, target, nil) }()
	receiveDemandEvent(t, lookup)
	s.mu.Lock()
	f := s.flights[target.Resource]
	s.mu.Unlock()
	if receipt, err := s.ImportVideo(ctx, source, map[string]bool{target.Resource: true}); err != nil || receipt == nil {
		t.Fatalf("import receipt=%v err=%v", receipt, err)
	}
	assertResponse(t, receiveDemandEvent(t, done), http.StatusOK, payload)
	receiveDemandEvent(t, f.done)
	if f.err != nil {
		t.Fatal(f.err)
	}
}

func TestImportedFileSuppliesBlockBeforeNextIP(t *testing.T) {
	s, _ := setup(t, nil, func(c *Config) { c.CacheUnlimited = true })
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	reading, closed := make(chan int64, 1), make(chan int64, 1)
	imported := make(chan struct{})
	var calls atomic.Int32
	s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
	var paths routeFixture
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		paths = append(paths, upstreamrequest.Candidate{Mode: "direct", Host: "play.udon.dance", IP: ip,
			Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				resp := fixtureRangeResponse(r, payload)
				if calls.Add(1) == 1 {
					resp.Body = &continuingRangeBody{ctx: r.Context(), ready: reading, closed: closed,
						gate: imported, remainder: strings.NewReader(payload[:1])}
				}
				return resp, nil
			})})
	}
	s.cfg.DirectCandidates = paths
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan *responseRecorder, 1)
	go func() { done <- requestContext(ctx, s, http.MethodGet, videoURL(payload), nil) }()
	receiveDemandEvent(t, reading)
	if receipt, err := s.ImportVideo(ctx, source, map[string]bool{videoKey(payload): true}); err != nil || receipt == nil {
		t.Fatal(receipt, err)
	}
	close(imported) // Let the first IP finish with a short body.
	assertResponse(t, receiveDemandEvent(t, done), http.StatusOK, payload)
	if got := calls.Load(); got != 1 {
		t.Fatalf("GET issued %d upstream requests, want 1", got)
	}
}

func TestSerialBlocksPreferDeliveringEndpoint(t *testing.T) {
	for _, outcome := range []string{"healthy", "origin_recovers", "final_recovers", "imported"} {
		t.Run(outcome, func(t *testing.T) {
			body := strings.Repeat("v", 2*int(rangeBlockSize))
			origin := videoURL(body)
			final := origin + "&endpoint=final"
			var requests []string
			s, _ := setup(t, nil, func(c *Config) { c.DownloadConcurrencySoftLimit = 1 })
			var source string
			if outcome == "imported" {
				source = filepath.Join(t.TempDir(), "video.mp4")
				if err := os.WriteFile(source, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					switch target {
					case origin:
						requests = append(requests, "origin")
						if len(requests) == 1 {
							return &http.Response{StatusCode: 307, Header: http.Header{"Location": {final}}, Body: http.NoBody, Request: r}, nil
						}
						if outcome == "final_recovers" {
							return &http.Response{StatusCode: 403, Body: http.NoBody, Request: r}, nil
						}
					case final:
						requests = append(requests, "final")
						if outcome == "imported" && len(requests) == 3 {
							// Both URLs are known before this block starts. Reject the final URL
							// after import so the old entry would be next in this same pass.
							if receipt, err := s.ImportVideo(r.Context(), source, map[string]bool{videoKey(body): true}); err != nil || receipt == nil {
								t.Errorf("import receipt=%v err=%v", receipt, err)
							}
							return &http.Response{StatusCode: 410, Body: http.NoBody, Request: r}, nil
						}
						if outcome != "healthy" && len(requests) == 3 {
							return &http.Response{StatusCode: 503, Body: http.NoBody, Request: r}, nil
						}
					}
					return fixtureRangeResponse(r, body), nil
				})}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			if _, err := s.Prefetch(ctx, origin); err != nil {
				t.Fatal(err)
			}
			want := "origin final final"
			if outcome == "origin_recovers" || outcome == "final_recovers" {
				want += " origin"
			}
			if outcome == "final_recovers" {
				want += " final"
			}
			if got := strings.Join(requests, " "); got != want {
				t.Fatalf("requests = %s, want %s", got, want)
			}
		})
	}
}

func TestDiscoveryContinuesWhileSavedAddressIsInUse(t *testing.T) {
	origin := videoURL(payload)
	saved, fresh := origin+"&address=saved", origin+"&address=fresh"
	usingSaved := make(chan struct{})
	enterSaved := sync.OnceFunc(func() { close(usingSaved) })
	s, _ := setup(t, nil, func(c *Config) {
		c.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			select {
			case <-usingSaved:
				return fresh, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	})
	if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: saved}); err != nil {
		t.Fatal(err)
	}
	s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			switch target {
			case saved:
				enterSaved()
				if err := waitForSharedAddress(r.Context(), s, videoKey(payload), fresh); err != nil {
					return nil, err
				}
				return nil, io.ErrUnexpectedEOF
			case fresh:
				return fixtureRangeResponse(r, payload), nil
			default:
				return &http.Response{StatusCode: 410, Body: http.NoBody, Request: r}, nil
			}
		})}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	assertResponse(t, requestContext(ctx, s, http.MethodGet, origin, nil), 200, payload)
}

func TestRedirectSharesTargetPathBackoff(t *testing.T) {
	body := strings.Repeat("v", 2*int(rangeBlockSize))
	origin := videoURL(body)
	final := origin + "&endpoint=final"
	s, _ := setup(t, nil, func(c *Config) { c.DownloadConcurrencySoftLimit = 1 })
	var requests []string
	s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
		if target == origin {
			return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, "origin")
				return &http.Response{StatusCode: 307, Header: http.Header{"Location": {final}}, Body: http.NoBody, Request: r}, nil
			})}
		}
		return []http.RoundTripper{
			resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, "final/1")
				if r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", rangeBlockSize-1) {
					return &http.Response{StatusCode: 503, Body: http.NoBody, Request: r}, nil
				}
				return fixtureRangeResponse(r, body), nil
			}),
			resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, "final/2")
				return fixtureRangeResponse(r, body), nil
			}),
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.Prefetch(ctx, origin); err != nil {
		t.Fatal(err)
	}
	if want := "origin final/1 final/1 origin final/2"; strings.Join(requests, " ") != want {
		t.Fatalf("requests = %v, want %s", requests, want)
	}
}

func TestLookupBudgetResumesRemainingSongs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{ctx: t.Context(), cfg: fixtureConfig()}
		fresh := videoURL(payload) + "&fresh=1"
		var calls []string
		releaseSecond := make(chan struct{})
		s.cfg.ResolvePlayback = func(_ context.Context, id string) (string, error) {
			calls = append(calls, id)
			switch id {
			case "1":
				time.Sleep(20 * time.Second)
				return videoURL(payload) + "&first=1", nil
			case "2":
				<-releaseSecond
				return "", context.DeadlineExceeded
			}
			return fresh, nil
		}

		// Start after the catalog has supplied the ordered song candidates.
		w := downloadSession{s: s, f: &flight{}}
		lookup := resourceAddresses{key: videoKey(payload), size: int64(len(payload)), preferredSong: "1",
			songs: []string{"1", "2", "3"}, tried: make(map[string]bool)}
		d := &downloadDemand{ctx: t.Context(), flight: w.f, lookup: lookup}
		s.demands = map[*downloadDemand]bool{d: true}
		a := &w.f.addresses
		a.attach(parsedVideo(t, s, payload))

		start := time.Now()
		w.refreshAddresses(t.Context(), true)
		a.wg.Wait()
		elapsed := time.Since(start)
		close(releaseSecond)
		s.wg.Wait()
		if elapsed != playbackResolveBudget || !slices.Equal(calls, []string{"1", "2"}) || len(a.urls) != 2 {
			t.Fatalf("first pass took %s, queries = %v, addresses = %v", elapsed, calls, a.urls)
		}
		time.Sleep(time.Until(a.lookups[lookupKey{song: "1"}].refresh.until))
		w.refreshAddresses(t.Context(), true)
		a.wg.Wait()
		s.wg.Wait()
		if !slices.Equal(calls, []string{"1", "2", "3"}) || len(a.urls) != 3 || a.urls[2].requestURL() != fresh {
			t.Fatalf("queries = %v, addresses = %v", calls, a.urls)
		}
	})
}

func TestFixedIPRecoveryWakesWaitingBlock(t *testing.T) {
	s, _ := setup(t, nil)
	h := NewDownloadRoutes(true, "")
	s.cfg.DownloadRoutes = h
	k := routeKey{"play.udon.dance", "192.0.2.1"}
	var calls atomic.Int32
	released, retried := make(chan struct{}), make(chan struct{})
	onRelease := sync.OnceFunc(func() { close(released) })
	s.cfg.BeginResourceLoad = func() func() { return onRelease }
	s.cfg.DirectCandidates = routeFixture{{Mode: "direct", Host: k.host, IP: k.ip,
		Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			switch calls.Add(1) {
			case 1:
				return nil, io.ErrUnexpectedEOF
			case 2:
				close(retried)
			}
			return fixtureRangeResponse(r, payload), nil
		})}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.Prefetch(ctx, videoURL(payload))
		done <- err
	}()
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("fixed IP did not finish its first request")
	}
	if rows := h.Snapshot(); len(rows) != 1 || !rows[0].Failed {
		t.Fatalf("missing shared failure: %v", rows)
	}
	h.record(k, "sample", 36, false, time.Millisecond)
	select {
	case <-retried:
	case <-time.After(650 * time.Millisecond):
		t.Fatal("block still waiting after the shared route recovered")
	}
	if err := <-done; err != nil || calls.Load() != 2 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestRejectedAddressYieldsToRecoveredURLOnSameIP(t *testing.T) {
	for _, status := range []int{403, 404, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, _ := setup(t, nil)
			b := videoURL(payload)
			a := b + "&address=a"
			if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: a}); err != nil {
				t.Fatal(err)
			}
			s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
			var rejectedIP, recoveredIP string
			failedOnB := make(map[string]bool)
			var paths routeFixture
			for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
				paths = append(paths, upstreamrequest.Candidate{Mode: "direct", Host: "play.udon.dance", IP: ip,
					Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						if r.URL.String() == b {
							if rejectedIP == "" {
								failedOnB[ip] = true
								return &http.Response{StatusCode: 503, Body: http.NoBody, Request: r}, nil
							}
							recoveredIP = ip
							return fixtureRangeResponse(r, payload), nil
						}
						if rejectedIP != "" {
							t.Error("retried A before the recovered B")
							return fixtureRangeResponse(r, payload), nil
						}
						// Let B's first cooldown expire while A is in flight.
						select {
						case <-time.After(1100 * time.Millisecond):
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
						rejectedIP = ip
						return &http.Response{StatusCode: status, Body: http.NoBody, Request: r}, nil
					})})
			}
			s.cfg.DirectCandidates = paths
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := s.Prefetch(ctx, b); err != nil {
				t.Fatal(err)
			}
			if recoveredIP != rejectedIP || !failedOnB[recoveredIP] {
				t.Fatalf("B initially failed on %v, A rejected on %q, B recovered on %q", failedOnB, rejectedIP, recoveredIP)
			}
		})
	}
}

func TestRedirectRespectsRejectedTargetBackoff(t *testing.T) {
	entry := videoURL(payload)
	target, fresh := entry+"&target=b", entry+"&target=c"
	secondPath := make(chan struct{})
	s, _ := setup(t, nil, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, _ string) (string, error) {
			select {
			case <-secondPath:
				return fresh, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	})
	var entries, targets atomic.Int32
	s.cfg.ResourceTransports = func(url string) []http.RoundTripper {
		request := resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			switch url {
			case entry:
				if entries.Add(1) == 2 {
					close(secondPath)
					if err := waitForSharedAddress(r.Context(), s, videoKey(payload), fresh); err != nil {
						return nil, err
					}
				}
				return &http.Response{StatusCode: 307, Header: http.Header{"Location": {target}}, Body: http.NoBody, Request: r}, nil
			case target:
				if targets.Add(1) > 1 {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 404, Body: http.NoBody, Request: r}, nil
			case fresh:
				return fixtureRangeResponse(r, payload), nil
			default:
				return nil, errors.New("unexpected resource URL")
			}
		})
		if url == entry {
			return []http.RoundTripper{request, request}
		}
		return []http.RoundTripper{request}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if _, err := s.prefetch(ctx, "1", entry); err != nil || entries.Load() != 2 || targets.Load() != 1 {
		t.Fatalf("entry calls=%d, rejected target calls=%d, err=%v", entries.Load(), targets.Load(), err)
	}
}

func TestRedirectFailureResumesOriginCandidates(t *testing.T) {
	s, _ := setup(t, nil)
	origin := videoURL(payload)
	var redirects, failed, backup atomic.Int32
	var failedAt time.Time
	s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
		if target != origin {
			return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				failed.Add(1)
				failedAt = time.Now()
				return &http.Response{StatusCode: 503, Body: http.NoBody, Request: r}, nil
			})}
		}
		return []http.RoundTripper{
			resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				redirects.Add(1)
				return &http.Response{StatusCode: 307, Header: http.Header{"Location": {origin + "&hop=1"}}, Body: http.NoBody, Request: r}, nil
			}),
			resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				backup.Add(1)
				if failedAt.IsZero() || time.Since(failedAt) >= time.Second {
					t.Error("origin backup did not run before the redirect target's retry")
				}
				return fixtureRangeResponse(r, payload), nil
			}),
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if _, err := s.Prefetch(ctx, origin); err != nil || redirects.Load() == 0 || failed.Load() == 0 || backup.Load() == 0 {
		t.Fatalf("redirects=%d failed=%d backup=%d err=%v", redirects.Load(), failed.Load(), backup.Load(), err)
	}
}

// All fixed IPs fail. The second IP recovers on its third turn, without a
// second scheduler submission or another request from the playback caller.
func TestBlockSurvivesRouteExhaustion(t *testing.T) {
	s, _ := setup(t, nil)
	s.cfg.DownloadRoutes = NewDownloadRoutes(true, "")
	var paths routeFixture
	var visits [8][]time.Time
	var mu sync.Mutex
	for i := range visits {
		paths = append(paths, upstreamrequest.Candidate{Mode: "direct", Host: "play.udon.dance", IP: fmt.Sprintf("192.0.2.%d", i+1),
			Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				visits[i] = append(visits[i], time.Now())
				if i == 1 && len(visits[i]) == 3 {
					return fixtureRangeResponse(r, payload), nil
				}
				if i%2 == 0 {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
				}
				return nil, io.ErrUnexpectedEOF
			})})
	}
	s.cfg.DirectCandidates = paths
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if _, err := s.Prefetch(ctx, videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, times := range visits {
		if len(times) < 2 || times[1].Sub(times[0]) < time.Second {
			t.Fatalf("IP %d retry times: %v", i, times)
		}
	}
	if delta := visits[1][2].Sub(visits[1][1]); delta < 2*time.Second {
		t.Fatalf("second backoff: %v", delta)
	}
	if delta := visits[7][0].Sub(visits[0][0]); delta >= time.Second {
		t.Fatalf("a fresh IP waited for another IP: %v", delta)
	}
}

func TestBlockCancellationDuringBackoffReleasesRequest(t *testing.T) {
	s, _ := setup(t, nil)
	release := make(chan struct{})
	called := 0
	s.cfg.BeginResourceLoad = func() func() { return sync.OnceFunc(func() { close(release) }) }
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(*http.Request) (*http.Response, error) { called++; return nil, io.ErrUnexpectedEOF })}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Prefetch(ctx, videoURL(payload)); done <- err }()
	select {
	case <-release:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt did not release")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("backoff ignored caller cancellation")
	}
	s.wg.Wait()
	if called != 1 {
		t.Fatalf("canceled task made %d requests", called)
	}
}
