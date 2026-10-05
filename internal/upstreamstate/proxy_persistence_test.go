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

func TestProxySampleRestoreRequiresStableMatchingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	at := time.Now().Add(-time.Minute)
	a := "stable-v1-" + strings.Repeat("a", 64)
	b := "stable-v1-" + strings.Repeat("b", 64)
	sample := ThroughputSample{ObservedAt: at, SongID: 42, Bytes: 16 << 20, Duration: 2 * time.Second}
	key := func(id string) string { return "resource/play.udon.dance/socks5/play.udon.dance/" + id }
	state := throughputState{LastAttempt: at, Samples: map[string]ThroughputSample{key(a): sample, key("1"): sample, key("2"): sample}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		pool, err := upstreamrequest.NewPool("socks5", nil, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("must not dial") }, id)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		ch := upstreamrequest.NewChannel()
		ch.Publish(&upstreamrequest.Transport{Pool: pool})
		m, err := newMonitor(Options{ThroughputStatePath: path}, ch)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if _, ok := m.throughput[key("1")]; ok {
			t.Fatal("legacy proxy sample restored")
		}
		if _, ok := m.throughput[key("2")]; ok {
			t.Fatal("legacy reconfigured proxy sample restored")
		}
		candidate := pool.Current("https://play.udon.dance")[0]
		recordFixture(m, observation{op: Resource, route: "play.udon.dance", channel: candidate.ID, state: "available", at: time.Now(), latency: time.Millisecond, songID: 73})
		r := m.Results(Resource)[0]
		if id == a {
			if r.LastThroughput == nil || r.ThroughputSongID != 42 || r.EstimatedSpeedBPS == nil {
				t.Fatal("same proxy lost restored sample", r)
			}
		} else if r.LastThroughput != nil || r.EstimatedSpeedBPS != nil {
			t.Fatal("different proxy inherited sample after light check", r)
		}
		if !m.lastThroughput.Equal(at) {
			t.Fatal("migration reset cooldown")
		}
	}
}
