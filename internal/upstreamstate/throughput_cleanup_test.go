package upstreamstate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type retainedCandidates struct {
	*candidateFixture
	ids   []string
	ready bool
}

func (p retainedCandidates) KnownProxyIDs() ([]string, bool) { return p.ids, p.ready }

func TestStandbyProxyThroughputSurvivesActiveSubset(t *testing.T) {
	standby := "stable-v1-" + strings.Repeat("c", 64)
	deleted := "stable-v1-" + strings.Repeat("d", 64)
	key := func(id string) string { return "resource/play.udon.dance/socks5/play.udon.dance/" + id }
	for _, ready := range []bool{false, true} {
		m := &Monitor{throughput: map[string]observation{key(standby): {}, key(deleted): {}}}
		p := retainedCandidates{ids: []string{standby}, ready: ready}
		m.pruneThroughputLocked(nil, map[string]bool{entry(Resource, "play.udon.dance"): true}, p)
		if _, ok := m.throughput[key(standby)]; !ok {
			t.Fatal("saved standby node lost history")
		}
		if _, ok := m.throughput[key(deleted)]; ok == ready {
			t.Fatal("deleted/unknown membership cleanup incorrect")
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
			keep := "resource/play.udon.dance/a/play.udon.dance"
			gone := "resource/play.udon.dance/b/play.udon.dance"
			at := time.Now().Add(-time.Minute)
			sample := ThroughputSample{ObservedAt: at, SongID: 42, Bytes: 16 << 20, Duration: 2 * time.Second}
			data, _ := json.Marshal(throughputState{SchemaVersion: throughputSchemaVersion, LastAttempt: at, Samples: map[string]ThroughputSample{keep: sample, gone: sample}})
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
