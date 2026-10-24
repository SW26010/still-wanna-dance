//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// A bounded live check of the actual DNS/bootstrap, ordinary API, pinned media
// and Monitor paths. It does not run throughput training or a download window.
func TestCFNetworkSmoke(t *testing.T) {
	if *cfexpOut == "" {
		t.Skip("explicit -cfexp-out required for live smoke check")
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal("output must be new:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	result := struct {
		Started   time.Time
		Complete  bool
		Bootstrap bool
		API       bool
		Media     map[string]string
		Monitor   bool
	}{Started: time.Now(), Media: map[string]string{}}
	defer func() {
		if err := cfexpSave(filepath.Join(dir, "smoke.json"), result); err != nil {
			t.Error(err)
		}
	}()
	dns := upstreamnet.NewResolver(network)
	defer dns.Close()
	if _, err := dns.ExperimentBootstrapProvider(ctx, "doh.pub"); err != nil {
		t.Fatal("DoH bootstrap failed")
	}
	result.Bootstrap = true
	ordinary := &http.Transport{DialContext: dns.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer ordinary.CloseIdleConnections()
	client := &http.Client{Transport: ordinary, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	target, err := cfexpPrepare(ctx, client, 1343)
	if err != nil {
		t.Fatal(err)
	}
	result.API = true
	pool, err := network.pool(dns)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cs, err := pool.Candidates(ctx, target.URL)
	if err != nil {
		t.Fatal("media candidate discovery failed")
	}
	for _, candidate := range cs {
		family := "IPv6"
		if net.ParseIP(candidate.IP).To4() != nil {
			family = "IPv4"
		}
		if result.Media[family] != "" {
			continue
		}
		r, _ := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
		r.Header.Set("Range", "bytes=0-1023")
		r.Header.Set("Accept-Encoding", "identity")
		resp, err := candidate.Transport.RoundTrip(r)
		if err != nil {
			t.Fatalf("%s pinned media request failed (see private connection log)", family)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1025))
		resp.Body.Close()
		if readErr != nil || len(body) != 1024 || resp.StatusCode != 206 || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-1023/%d", target.Size) || resp.TLS == nil || len(resp.TLS.VerifiedChains) == 0 {
			t.Fatalf("%s Range/TLS validation failed", family)
		}
		result.Media[family] = fmt.Sprintf("%x", sha256.Sum256(body))
		t.Logf("%s pinned HTTPS: verified TLS, 206, 1024 bytes", family)
	}
	if len(result.Media) != 2 || result.Media["IPv4"] != result.Media["IPv6"] {
		t.Fatal("dual-stack content check incomplete or inconsistent")
	}
	channel := upstreamrequest.NewChannel()
	channel.Publish(&cfexpProvider{api: pool, resource: pool, ordinary: ordinary})
	m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel, PlaybackSongCandidates: func(context.Context) []int64 { return []int64{1343} }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.CheckSelected(ctx, upstreamstate.CheckPlayback); err != nil {
		t.Fatal("Monitor check failed")
	}
	if err := cfexpSave(filepath.Join(dir, "monitor-private.json"), m.Snapshot()); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Results(upstreamstate.PlaybackURL) {
		if r.State == "available" {
			result.Monitor = true
		}
	}
	if !result.Monitor {
		t.Fatal("Monitor found no available playback API path")
	}
	if err := network.err(); err != nil {
		t.Fatal(err)
	}
	result.Complete = true
}
