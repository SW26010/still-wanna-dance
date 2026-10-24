package console

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestSetupMonitorRetainsTraversalAcrossRunsAndCancellation(t *testing.T) {
	// IDs outside the bundled usage priors make equal-score ordering explicit.
	const low, middle, high int64 = 900001, 900002, 900003
	for _, upstream := range []string{"direct", "proxy"} {
		t.Run(upstream, func(t *testing.T) {
			c := testConsole(t)
			stubSetupNetwork(t, c)
			input := setupCalibrationRequest{Upstream: upstream}
			channel, client, monitor := c.setupNetwork.channel, c.setupNetwork.client, c.setupNetwork.monitor
			if upstream == "proxy" {
				version, err := c.setupSettingsVersion(c.savedSettings)
				if err != nil {
					t.Fatal(err)
				}
				input.Proxy = &setupPlan{BaseVersion: version, ProxyBackend: "external-socks5", SOCKS5Address: "127.0.0.1:9"}
				settings, err := c.setupDiagnosticProxy(false, input)
				if err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				monitor, err = c.setupConnectivityMonitorLocked(upstream, settings)
				c.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				channel, client = c.setupNetwork.proxy.channel, c.setupNetwork.proxy.client
				client.Transport.(interface{ Retire() }).Retire()
			}
			var mu sync.Mutex
			var requested []int64
			var lists, resources int
			var blocked, recoverHigh bool
			entered := make(chan struct{}, 2)
			transport := catalogTransport(func(r *http.Request) (*http.Response, error) {
				body, status := "", http.StatusOK
				header := make(http.Header)
				switch r.URL.Path {
				case "/Api/Songs/list":
					// Deliberately different from local candidates: this must only
					// be used after the database has no missing metadata.
					body = `{"groups":{"contents":[{"songInfos":[{"id":99}]}]}}`
					mu.Lock()
					lists++
					mu.Unlock()
				case "/api/v2/wanna/songs", "/api/wannaInfo":
					var entries []string
					for _, id := range []int64{low, middle, high} {
						entries = append(entries, fmt.Sprintf(`{"id":%d,"checksum":"%032x"}`, id, id))
					}
					body = `{"code":200,"data":{"time":"20261016000000","groups":[{"entries":[` + strings.Join(entries, ",") + `]}]}}`
					mu.Lock()
					lists++
					mu.Unlock()
				case "/Api/Songs/play":
					id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
					mu.Lock()
					requested = append(requested, id)
					block, recovered := blocked, recoverHigh
					mu.Unlock()
					if block {
						entered <- struct{}{}
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					if id == high && !recovered { // Highest-priority failure must not starve other IDs.
						status = http.StatusServiceUnavailable
					} else {
						status = http.StatusFound
						header.Set("Location", fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=1234", id, id))
					}
				default:
					if r.Header.Get("Range") != "bytes=0-0" {
						t.Error("unexpected resource request", r.Header.Get("Range"))
					}
					mu.Lock()
					resources++
					mu.Unlock()
					body, status = "x", http.StatusPartialContent
					header.Set("Content-Range", "bytes 0-0/1234")
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			client.Transport = transport
			channel.Publish(transport)
			before := upstreamrequest.Default.Snapshot()
			// The completed middle ID leaves the set. Canceling the low ID keeps
			// the pass position, then the next pass retries high before low.
			for round, want := range []int64{high, middle, low, high, low, high, high, 99} {
				mu.Lock()
				blocked, requested = round == 2, nil
				recoverHigh = round >= 6
				mu.Unlock()
				if err := c.controlSetupConnectivity(false, input); err != nil {
					t.Fatal(err)
				}
				if round == 2 {
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("playback did not start")
					}
					if err := c.controlSetupConnectivity(true, setupCalibrationRequest{}); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case <-c.setupNetwork.connectivityDone:
				case <-time.After(5 * time.Second):
					t.Fatal("batch did not drain")
				}
				mu.Lock()
				ids := slices.Clone(requested)
				mu.Unlock()
				if len(ids) == 0 || (round != 2 && len(ids) != 2) {
					t.Fatalf("round %d requests: %v", round, ids)
				}
				for _, id := range ids {
					if id != want {
						t.Fatalf("round %d requested %v, want %d", round, ids, want)
					}
				}
				if state := monitor.Snapshot(); state.Closed || state.Checking || state.Scheduled || !state.LastThroughputAttempt.IsZero() {
					t.Fatal("monitor lost lifecycle or lightweight scope", state)
				}
			}
			pending := c.monitorPlaybackSongCandidates(context.Background())
			if len(pending) != 0 || lists != 24 || resources != 4 {
				t.Fatalf("pending=%v lists=%d resources=%d", pending, lists, resources)
			}
			for _, id := range []int64{low, middle, high} {
				for _, node := range []string{"cf", "nya"} {
					urls, err := c.service.SongURLs(context.Background(), id, node)
					if err != nil || len(urls) != 1 {
						t.Fatalf("metadata not persisted: %d %s: %v %v", id, node, urls, err)
					}
				}
			}
			if after := upstreamrequest.Default.Snapshot(); before.Transport != after.Transport || before.Revision != after.Revision {
				t.Fatal("wizard changed business routing")
			}
		})
	}
}

func TestSetupProxyMonitorReusesOnlyMatchingDraft(t *testing.T) {
	c := testConsole(t)
	stubSetupNetwork(t, c)
	direct := c.setupNetwork.monitor
	settings := c.settings
	settings.UpstreamMode, settings.ProxyBackend = "proxy", "external-socks5"
	settings.SOCKS5Address = "127.0.0.1:9"
	settings.SOCKS5Username, settings.SOCKS5Password = "fixture", "secret"
	selectMonitor := func(s Settings) (*upstreamstate.Monitor, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.setupConnectivityMonitorLocked("proxy", s)
	}
	first, err := selectMonitor(settings)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := selectMonitor(settings); err != nil || same != first {
		t.Fatal("same draft discarded traversal", err)
	}
	old := c.setupNetwork.proxy
	settings.SOCKS5Password = "changed"
	second, err := selectMonitor(settings)
	if err != nil || second == first || !first.Snapshot().Closed || old.channel.Snapshot().Transport != nil {
		t.Fatal("changed credentials reused the old scope", err)
	}
	settings.SOCKS5Address = "invalid"
	if _, err := selectMonitor(settings); err == nil || c.setupNetwork.proxy.monitor != second || second.Snapshot().Closed {
		t.Fatal("failed replacement destroyed the current scope", err)
	}
	if c.setupNetwork.monitor != direct || direct.Snapshot().Closed || testDNS(c).queries.Load() != 0 {
		t.Fatal("proxy selection changed direct scope or queried DNS")
	}
	c.Close()
	if !second.Snapshot().Closed || !direct.Snapshot().Closed {
		t.Fatal("shutdown did not close retained monitors")
	}
}
