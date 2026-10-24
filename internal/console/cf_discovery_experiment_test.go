//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type cfDiscoverySnapshot = upstreamnet.ExperimentDNSSnapshot

func cfDiscoveryState(d *upstreamnet.Resolver, host string) cfDiscoverySnapshot {
	return d.ExperimentSnapshot(host)
}

type cfDiscoveryEvent struct {
	Started, Finished      time.Time
	Host, Operation, Error string
	IPs                    []string
	Before, After          cfDiscoverySnapshot
}
type cfDiscoveryRequest struct {
	Started, Finished       time.Time
	Host, IP, Remote, Error string
	Status                  int
}
type cfDiscoveryTLS struct {
	Started, Finished       time.Time
	Event                   int
	Host, IP, Remote, Error string
	OK                      bool
}
type cfDiscoveryProvider struct {
	base     *cfexpProvider
	r        *cfDNSRecorder
	mu       sync.Mutex
	events   []cfDiscoveryEvent
	requests []cfDiscoveryRequest
}

func (p *cfDiscoveryProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	return p.base.RoundTrip(r)
}
func (p *cfDiscoveryProvider) Changed() <-chan struct{} { return p.base.Changed() }
func (p *cfDiscoveryProvider) wrap(cs []upstreamrequest.Candidate) []upstreamrequest.Candidate {
	for i := range cs {
		c := cs[i]
		cs[i].Transport = cfexpFunc(func(req *http.Request) (*http.Response, error) {
			row := cfDiscoveryRequest{Started: time.Now(), Host: req.URL.Hostname(), IP: c.IP}
			trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				row.Remote, _, _ = net.SplitHostPort(info.Conn.RemoteAddr().String())
			}}
			resp, err := c.Transport.RoundTrip(req.Clone(httptrace.WithClientTrace(req.Context(), trace)))
			row.Finished = time.Now()
			if err != nil {
				row.Error = "transport_failed"
			}
			if resp != nil {
				row.Status = resp.StatusCode
			}
			p.mu.Lock()
			p.requests = append(p.requests, row)
			p.mu.Unlock()
			return resp, err
		})
	}
	return cs
}
func (p *cfDiscoveryProvider) Candidates(ctx context.Context, target string) ([]upstreamrequest.Candidate, error) {
	if p.base.pool(target) == nil {
		return nil, nil
	}
	u, _ := url.Parse(target)
	row := cfDiscoveryEvent{Started: time.Now(), Host: u.Hostname(), Operation: "discovery", Before: cfDiscoveryState(p.r.d, u.Hostname())}
	cs, err := p.base.Candidates(ctx, target)
	row.Finished, row.After = time.Now(), cfDiscoveryState(p.r.d, u.Hostname())
	if err != nil {
		row.Error = "discovery_failed"
	}
	for _, c := range cs {
		row.IPs = append(row.IPs, c.IP)
	}
	p.mu.Lock()
	p.events = append(p.events, row)
	p.mu.Unlock()
	return p.wrap(cs), err
}
func (p *cfDiscoveryProvider) Current(target string) []upstreamrequest.Candidate {
	cs := p.base.Current(target)
	if len(cs) == 0 && p.base.pool(target) != nil {
		u, _ := url.Parse(target)
		row := cfDiscoveryEvent{Started: time.Now(), Finished: time.Now(), Host: u.Hostname(), Operation: "current_empty", After: cfDiscoveryState(p.r.d, u.Hostname())}
		p.mu.Lock()
		p.events = append(p.events, row)
		p.mu.Unlock()
	}
	return p.wrap(cs)
}

func cfDiscoveryCheckTLS(ctx context.Context, network *cfNetwork, host, ip string, event int) cfDiscoveryTLS {
	row := cfDiscoveryTLS{Started: time.Now(), Host: host, IP: ip, Event: event}
	// Preserve the former tls.Dialer's three-second dial/handshake budget.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := network.DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	if err == nil {
		secured := tls.Client(c, &tls.Config{ServerName: host})
		err = secured.HandshakeContext(ctx)
		defer secured.Close()
	}
	if err == nil {
		row.Remote, _, _ = net.SplitHostPort(c.RemoteAddr().String())
		row.OK = row.Remote == ip
		c.Close()
	} else {
		row.Error = "tls_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			row.Error = "timeout"
		}
	}
	row.Finished = time.Now()
	return row
}

func TestCFDiscoveryExperiment(t *testing.T) {
	if *cfexpOut == "" {
		t.Skip("explicit -cfexp-out required")
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal("output must be new:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	r := &cfDNSRecorder{d: upstreamnet.NewResolver(network)}
	defer r.d.Close()
	pool, err := network.pool(r)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ordinary := &http.Transport{DialContext: r.d.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer ordinary.CloseIdleConnections()
	p := &cfDiscoveryProvider{r: r, base: &cfexpProvider{api: pool, resource: pool, ordinary: ordinary}}
	channel := upstreamrequest.NewChannel()
	channel.Publish(p)
	m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel, PlaybackSongCandidates: func(context.Context) []int64 { return []int64{1343} }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var checks []cfDiscoveryTLS
	started := time.Now()
	outcome := "running"
	save := func() {
		p.mu.Lock()
		events := slices.Clone(p.events)
		requests := slices.Clone(p.requests)
		p.mu.Unlock()
		r.mu.Lock()
		lookups := slices.Clone(r.lookups)
		answers := slices.Clone(r.answers)
		r.mu.Unlock()
		if err := cfexpSave(filepath.Join(dir, "discovery.json"), struct {
			Started, Saved time.Time
			Outcome        string
			Events         []cfDiscoveryEvent
			Requests       []cfDiscoveryRequest
			TLS            []cfDiscoveryTLS
			Lookups        []cfDNSLookup
			Answers        []cfDNSAnswer
		}{started, time.Now(), outcome, events, requests, checks, lookups, answers}); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if outcome == "running" {
			outcome = "failed"
		}
		save()
	}()
	known := map[string][]string{}
	probed := map[string]int{}
	cursor := 0
	for round := 0; round < 72; round++ {
		for _, host := range []string{"api.udon.dance", cfexpHost} {
			target := "https://" + host
			if host == "api.udon.dance" {
				target += "/Api/Songs/play?node=cf&id=1343"
			}
			cs, _ := p.Candidates(ctx, target)
			if len(known[host]) == 0 {
				families := map[bool]bool{}
				for _, c := range cs {
					v4 := net.ParseIP(c.IP).To4() != nil
					if !families[v4] {
						known[host] = append(known[host], c.IP)
						families[v4] = true
					}
				}
			}
		}
		if round%6 == 0 {
			if err := m.CheckSelected(ctx, upstreamstate.CheckPlayback); err != nil {
				t.Logf("monitor round=%d failed", round)
			}
			if err := cfexpSave(filepath.Join(dir, "monitor-"+time.Now().Format("150405.000")+"-private.json"), m.Snapshot()); err != nil {
				t.Fatal(err)
			}
		}
		p.mu.Lock()
		pending := slices.Clone(p.events[cursor:])
		first := cursor
		cursor = len(p.events)
		p.mu.Unlock()
		for i, e := range pending {
			if len(e.IPs) != 0 || probed[e.Host] >= 3 || len(known[e.Host]) == 0 {
				continue
			}
			probed[e.Host]++
			t.Logf("empty host=%s operation=%s error=%s cache_remaining=%.0fms", e.Host, e.Operation, e.Error, e.After.CacheUntil.Sub(e.After.At).Seconds()*1000)
			for _, ip := range known[e.Host] {
				result := cfDiscoveryCheckTLS(ctx, network, e.Host, ip, first+i)
				checks = append(checks, result)
				t.Logf("tls host=%s ip=%s ok=%v", e.Host, ip, result.OK)
			}
		}
		save()
		if round%6 == 0 {
			t.Logf("round=%d/72", round+1)
		}
		if err := cfexpPause(ctx, 5*time.Second); err != nil {
			t.Fatal("observation_context")
		}
	}
	m.Close()
	pool.Close()
	r.d.Close()
	if err := cfexpPause(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	outcome = "complete"
}

func TestCFDiscoveryHarness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := upstreamnet.NewResolver(nil)
		defer d.Close()
		ctx := context.Background()
		ips := []string{"104.26.4.28", "2606:4700:20::681a:41c"}
		_, err := d.ExperimentLookupShared(ctx, cfexpHost, func(context.Context) ([]string, time.Duration, error) { return ips, time.Second, nil })
		if err != nil {
			t.Fatal(err)
		}
		before := cfDiscoveryState(d, cfexpHost)
		time.Sleep(2 * time.Second)
		after := cfDiscoveryState(d, cfexpHost)
		if len(d.CurrentAddresses(cfexpHost)) != 0 || len(after.CacheIPs) != 2 || len(after.Leases) != 2 || !before.At.Before(before.CacheUntil) || after.At.Before(after.CacheUntil) {
			t.Fatal("snapshot must distinguish known addresses from live membership")
		}
		before.Leases[ips[0]] = time.Time{}
		if cfDiscoveryState(d, cfexpHost).Leases[ips[0]].IsZero() {
			t.Fatal("snapshot aliases resolver state")
		}
	})
}
