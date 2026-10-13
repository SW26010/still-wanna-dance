package console

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Expose the full active pool but probe only two proxies, as when a check is
// interrupted or new channels have not been measured yet. Do not expose Pool's
// readiness discovery, which would bypass this restricted Candidates call.
type partialMonitorChannels struct {
	pool upstreamrequest.Candidates
	http.RoundTripper
}

func (p partialMonitorChannels) Current(target string) []upstreamrequest.Candidate {
	return p.pool.Current(target)
}

func (p partialMonitorChannels) Changed() <-chan struct{} { return p.pool.Changed() }

func (p partialMonitorChannels) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	var out []upstreamrequest.Candidate
	for _, c := range p.Current(target) {
		if c.Mode == "socks5" && len(out) < 2 {
			c.Transport = p.RoundTripper
			out = append(out, c)
		}
	}
	return out, nil
}

type timedSampleBody struct{ io.ReadCloser }

func (b timedSampleBody) Read(p []byte) (int, error) {
	time.Sleep(time.Millisecond) // Keep throughput measurable on Windows clocks.
	return b.ReadCloser.Read(p)
}

func TestMihomoPartialObservationsKeepActiveFallbacks(t *testing.T) {
	for _, mode := range []string{"proxy", "cold-auto", "warm-auto"} {
		t.Run(mode, func(t *testing.T) {
			if !consoleMihomoProcess(t) {
				return
			}
			c := testConsole(t)
			s := runtimeNodeSettings(t, c, 8) // Three saved standby nodes must stay out.
			if mode != "proxy" {
				s.UpstreamMode = "auto"
			}
			_, client, err := c.networkFor(s)
			if err != nil {
				t.Fatal(err)
			}
			c.client.Transport.(interface{ Retire() }).Retire()
			c.client, c.settings = client, s
			runtime := client.Transport.(*mihomoTransport)
			if mode != "cold-auto" {
				c.dns.cache = make(map[string]dnsEntry)
				for _, host := range []string{"api.udon.dance", "play.udon.dance"} {
					c.dns.cache[host] = dnsEntry{ips: []string{"127.0.0.1", "127.0.0.2", "127.0.0.3", "127.0.0.4", "127.0.0.5", "127.0.0.6"}, until: time.Now().Add(time.Hour)}
				}
			}
			var probeCalls atomic.Int32
			probe := catalogTransport(func(r *http.Request) (*http.Response, error) {
				probeCalls.Add(1)
				time.Sleep(time.Millisecond)
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))}
				switch {
				case r.URL.Path == "/Api/Songs/play":
					resp.StatusCode = 302
					resp.Header.Set("Location", "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=131072")
				case r.Header.Get("Range") != "":
					resp.StatusCode = 206
					resp.Header.Set("Content-Range", "bytes 0-131071/131072")
					resp.Body = timedSampleBody{io.NopCloser(strings.NewReader(strings.Repeat("x", 131072)))}
				}
				return resp, nil
			})
			channel := upstreamrequest.NewChannel()
			channel.Publish(partialMonitorChannels{runtime, probe})
			m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel})
			if err != nil {
				t.Fatal(err)
			}
			c.monitor = m
			if err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := probeCalls.Load()
			for _, tc := range []struct {
				op upstreamstate.Operation
				q  upstreamstate.Constraints
			}{
				{upstreamstate.Catalog, upstreamstate.Constraints{Entry: c.apiBase + "/Api/Songs/list"}},
				{upstreamstate.PlaybackURL, upstreamstate.Constraints{Entry: c.apiBase + "/Api/Songs/play?node=cf", Route: "cf"}},
				{upstreamstate.Resource, upstreamstate.Constraints{Target: "https://play.udon.dance/files/1/42-video.mp4"}},
			} {
				t.Run(string(tc.op), func(t *testing.T) {
					measured := m.Selections(tc.op, tc.q)
					if len(measured) != 2 {
						t.Fatalf("fixture needs exactly two recommendable channels: %+v", measured)
					}
					got := c.operationSelections(tc.op, tc.q)
					want := 6
					if mode == "proxy" {
						want = 5
					}
					if len(got) != want || len(got) > upstreamrequest.MaxOperationCandidates {
						t.Fatalf("partial observations lost active fallbacks: got %d want %d", len(got), want)
					}
					seen := make(map[string]bool)
					var proxies, direct int
					for i, selection := range got {
						if seen[selection.Channel.ID] {
							t.Fatal("duplicate candidate", selection.Channel.ID)
						}
						seen[selection.Channel.ID] = true
						if i < len(measured) {
							if selection.Channel.ID != measured[i].Channel.ID || selection.Result.ChannelID != measured[i].Result.ChannelID {
								t.Fatal("measured preference order changed")
							}
						} else if selection.Result.ChannelID != "" || selection.Result.EstimatedSpeedBPS != nil {
							t.Fatal("fallback invented a measurement")
						}
						if selection.Channel.Mode == "socks5" {
							proxies++
							id := selection.Channel.ID[strings.LastIndex(selection.Channel.ID, "/")+1:]
							if !slices.Contains(runtime.known[:5], id) {
								t.Fatal("standby node promoted", id)
							}
						} else {
							direct++
							if mode == "cold-auto" && selection.Channel.ID != "" || mode == "warm-auto" && selection.Channel.ID == "" {
								t.Fatal("wrong cold/cached direct fallback")
							}
						}
					}
					if proxies != 5 || direct != want-5 {
						t.Fatalf("mode isolation/budget: proxies=%d direct=%d", proxies, direct)
					}
				})
			}
			if probeCalls.Load() != before {
				t.Fatal("business selection triggered a probe")
			}
		})
	}
}

func TestBusinessResolutionReservesFallbackDeadline(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "auto"
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
	var business atomic.Bool
	var attempts atomic.Int32
	measured := catalogTransport(func(r *http.Request) (*http.Response, error) {
		if business.Load() && attempts.Add(1) == 1 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))}
		if r.URL.Path == "/Api/Songs/play" {
			resp.StatusCode = 302
			resp.Header.Set("Location", target)
		}
		return resp, nil
	})
	c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{measured})
	m, err := upstreamstate.NewMonitor(upstreamstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.monitor = m
	if err := m.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
		t.Fatal(err)
	}
	if n := len(c.operationClients(upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: "cf", Entry: c.apiBase + "/Api/Songs/play?node=cf"})); n != 2 {
		t.Fatalf("want two measured channels, got %d", n)
	}
	business.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := c.resolvePlayback(ctx, "42", "cf", "cf")
	if err != nil || got != target || attempts.Load() != 2 {
		t.Fatalf("target=%q attempts=%d err=%v", got, attempts.Load(), err)
	}
}

func TestBusinessResolutionUsesMeasuredTransport(t *testing.T) {
	c := testConsole(t)
	cfg := fixtureCacheConfig()
	cfg.StorageDir = c.settings.StorageDir
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
	measured := catalogTransport(func(r *http.Request) (*http.Response, error) {
		time.Sleep(time.Millisecond)
		resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))}
		if r.URL.Path == "/Api/Songs/play" {
			resp.StatusCode = 302
			resp.Header.Set("Location", target)
		}
		return resp, nil
	})
	c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{measured})
	m, err := upstreamstate.NewMonitor(upstreamstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.monitor = m
	if err := m.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
		t.Fatal(err)
	}
	if len(m.Selections(upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: "cf"})) == 0 {
		t.Fatal("fixture has no measured playback channel")
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Error("business request reselected an unmeasured transport")
		return nil, errors.New("unmeasured transport")
	})
	got, err := c.resolvePlayback(context.Background(), "42", "cf", "cf")
	if err != nil || got != target {
		t.Fatalf("target=%q err=%v", got, err)
	}
	urls, err := engine.SongURLs(context.Background(), 42, "cf")
	if err != nil || len(urls) != 1 || urls[0].URL != target || urls[0].API != c.apiBase+"/Api/Songs/play" {
		t.Fatalf("API observation not persisted: %+v %v", urls, err)
	}
}
