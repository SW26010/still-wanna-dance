package upstreamstate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type cleanupDNS struct{ err error }

func (d *cleanupDNS) Addresses(context.Context, string) ([]upstreamrequest.Address, error) {
	return nil, d.err
}
func (d *cleanupDNS) CurrentAddresses(string) []upstreamrequest.Address { return nil }
func (d *cleanupDNS) DNSChanged() <-chan struct{}                       { return nil }

type fallbackDiscovery struct{ *upstreamrequest.Transport }

func (p fallbackDiscovery) CandidatesWithReadiness(ctx context.Context, target string) ([]upstreamrequest.Candidate, upstreamrequest.CandidateReadiness, error) {
	cs, ready, err := p.Pool.CandidatesWithReadiness(ctx, target)
	for i := range cs {
		cs[i].Transport = transportFunc(fixture)
	}
	return cs, ready, err
}

func TestProxyFallbackDoesNotPruneRestoredDirectThroughput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	direct := "resource/cf/direct/play.udon.dance/192.0.2.1"
	oldProxy := "resource/cf/socks5/play.udon.dance/stable-v1-" + strings.Repeat("a", 64)
	at := time.Now().Add(-time.Minute)
	sample := ThroughputSample{ObservedAt: at, SongID: 42, Bytes: 16 << 20, Duration: 2 * time.Second}
	data, _ := json.Marshal(throughputState{LastAttempt: at, Samples: map[string]ThroughputSample{direct: sample, oldProxy: sample}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	dns := &cleanupDNS{err: errors.New("DNS unavailable")}
	pool, err := upstreamrequest.NewPool("auto", dns, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unexpected network") }, "stable-v1-"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ch := upstreamrequest.NewChannel()
	ch.Publish(fallbackDiscovery{&upstreamrequest.Transport{Pool: pool}})
	m, err := newMonitor(Options{ThroughputStatePath: path}, ch)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, failed := range []bool{true, false} {
		if !failed {
			dns.err = nil
		}
		if err := m.CheckSelected(context.Background(), CheckLatency); err != nil {
			t.Fatal(err)
		}
		if _, ok := m.throughput[direct]; ok != failed {
			t.Fatal("incorrect direct cleanup after DNS lookup", failed)
		}
		if _, ok := m.throughput[oldProxy]; ok {
			t.Fatal("old proxy retained despite known configuration")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved throughputState
		if err = json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if _, ok := saved.Samples[direct]; ok != failed {
			t.Fatal("incorrect persisted direct history", failed)
		}
		if _, ok := saved.Samples[oldProxy]; ok {
			t.Fatal("old proxy persisted")
		}
	}
}

type coldResourceCandidates struct {
	*candidateFixture
	ready, fail bool
}

func resourceTarget(target string) bool {
	return strings.Contains(target, "play.udon.dance") || strings.Contains(target, "nya.xin.moe")
}

func (p *coldResourceCandidates) Current(target string) []upstreamrequest.Candidate {
	if resourceTarget(target) && !p.ready {
		return nil
	}
	return p.candidateFixture.Current(target)
}

func (p *coldResourceCandidates) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	if resourceTarget(target) {
		if p.fail {
			return nil, errors.New("DNS unavailable")
		}
		p.ready = true
	}
	return p.Current(target), nil
}

func TestRestoredThroughputPrunedOnlyAfterResourceDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        CheckKind
		fail, prune bool
	}{
		{"ready", CheckLatency, false, true},
		{"unrelated check", CheckCatalog, false, false},
		{"failed discovery", CheckLatency, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "throughput.json")
			keep := "resource/cf/a/play.udon.dance"
			gone := "resource/cf/b/play.udon.dance"
			at := time.Now().Add(-time.Minute)
			sample := ThroughputSample{ObservedAt: at, SongID: 42, Bytes: 16 << 20, Duration: 2 * time.Second}
			data, _ := json.Marshal(throughputState{LastAttempt: at, Samples: map[string]ThroughputSample{keep: sample, gone: sample}})
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			provider := &coldResourceCandidates{candidateFixture: &candidateFixture{expires: time.Now().Add(time.Hour), changed: make(chan struct{})}, fail: tc.fail}
			ch := upstreamrequest.NewChannel()
			ch.Publish(provider)
			m, err := newMonitor(Options{ThroughputStatePath: path}, ch)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.Snapshot() // Cold read must not discard restored samples.
			if len(m.history) != 0 || len(m.throughput) != 2 {
				t.Fatal("restored data lost before discovery")
			}
			if err = m.CheckSelected(context.Background(), tc.kind); err != nil {
				t.Fatal(err)
			}
			_, present := m.throughput[gone]
			if present == tc.prune {
				t.Fatal("incorrect memory cleanup", present)
			}
			if _, ok := m.throughput[keep]; !ok {
				t.Fatal("active candidate sample removed")
			}
			data, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved throughputState
			if err = json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			_, present = saved.Samples[gone]
			if present == tc.prune {
				t.Fatal("cleanup not persisted", present)
			}
			if !saved.LastAttempt.Equal(at) {
				t.Fatal("cleanup reset cooldown")
			}
		})
	}
}
