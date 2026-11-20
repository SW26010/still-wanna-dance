//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamnet"
)

const cfSelectionBudget int64 = 3 << 30

type cfSelectionCandidate struct {
	IP, Source string
}

type cfSelectionProbe struct {
	IP, Kind, Error, ErrorDetail, Remote, Cache, Age, Colo, Hash string
	Bytes                                                        int64
	ElapsedMS, TCPMS, TLSMS, HeadersMS                           float64
	Status                                                       int
	TLSVerified                                                  bool
}

type cfSelectionWindow struct {
	Schema                               int
	Method                               string
	Started, Finished                    time.Time
	Outcome, Error                       string
	Candidates                           []cfSelectionCandidate
	DNSIPs                               []string
	Probes                               []cfSelectionProbe
	Training, Evaluation                 []cfexpRun
	Selected                             map[string]string
	Unqualified                          []string
	BodyBytes                            int64
	PreparationMS, ScreenMS, ReferenceMS float64
}

// Official IPv4 scope snapshot, checked 2026-10-22. Sampling is finite and
// independent of performance; no assumption that a prefix is one route.
func cfSelectionCandidates(dns []string) []cfSelectionCandidate {
	var out []cfSelectionCandidate
	add := func(ip, source string) {
		if !slices.ContainsFunc(out, func(c cfSelectionCandidate) bool { return c.IP == ip }) {
			out = append(out, cfSelectionCandidate{ip, source})
		}
	}
	for _, ip := range dns {
		add(ip, "dns")
	}
	for _, ip := range []string{"103.21.244.1", "104.16.0.1", "104.24.0.1", "172.64.144.1"} {
		add(ip, "historical")
	}
	prefixes := []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"}
	rng := rand.New(rand.NewPCG(20261022, 21))
	rng.Shuffle(len(prefixes), func(i, j int) { prefixes[i], prefixes[j] = prefixes[j], prefixes[i] })
	for _, raw := range prefixes[:4] {
		p := netip.MustParsePrefix(raw)
		b := p.Addr().As4()
		u := binary.BigEndian.Uint32(b[:]) | rng.Uint32N(uint32(1)<<(32-p.Bits()))
		binary.BigEndian.PutUint32(b[:], u)
		add(netip.AddrFrom4(b).String(), "sampled")
	}
	return out
}

func cfSelectionHashes(r cfexpRun) map[int64]string {
	out := map[int64]string{}
	for _, q := range r.Requests {
		if q.Error == "" {
			out[q.Offset] = q.Hash
		}
	}
	return out
}

// Each probe owns a new pool: both sizes include a cold TCP/TLS connection.
func cfSelectionShort(ctx context.Context, network *cfNetwork, target cfexpTarget, ip string, length int64, reference map[int64]string, total *atomic.Int64, stop context.CancelFunc) cfSelectionProbe {
	r := cfSelectionProbe{IP: ip, Kind: fmt.Sprintf("single-%d", length)}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tr, closePool, err := cfFamilyTransport(ctx, network, cfexpStatic{[]string{ip}, time.Now().Add(time.Minute)}, target.URL, ip)
	if err != nil {
		r.Error = "pool"
		r.ErrorDetail = err.Error()
		return r
	}
	defer closePool()
	var tcpStart, tlsStart time.Time
	trace := &httptrace.ClientTrace{
		ConnectStart:      func(_, _ string) { tcpStart = time.Now() },
		ConnectDone:       func(_, _ string, _ error) { r.TCPMS = cfexpMS(time.Since(tcpStart)) },
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone:  func(_ tls.ConnectionState, _ error) { r.TLSMS = cfexpMS(time.Since(tlsStart)) },
		GotConn:           func(c httptrace.GotConnInfo) { r.Remote, _, _ = net.SplitHostPort(c.Conn.RemoteAddr().String()) },
	}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", target.URL, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", length-1))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := tr.RoundTrip(req)
	r.HeadersMS = cfexpMS(time.Since(start))
	if err != nil {
		r.Error = "transport"
		r.ErrorDetail = err.Error()
		r.Remote = ""
		r.ElapsedMS = r.HeadersMS
		return r
	}
	defer resp.Body.Close()
	r.Status, r.Cache, r.Age = resp.StatusCode, resp.Header.Get("CF-Cache-Status"), resp.Header.Get("Age")
	r.TLSVerified = resp.TLS != nil && len(resp.TLS.VerifiedChains) > 0
	if ray := resp.Header.Get("CF-Ray"); strings.Contains(ray, "-") {
		r.Colo = ray[strings.LastIndexByte(ray, '-')+1:]
	}
	if r.Status == 429 || r.Status == 503 {
		r.Error = "stop_status"
		stop()
	} else if r.Status != 206 {
		r.Error = "status"
	} else if !r.TLSVerified || r.Remote != ip || resp.ContentLength != length || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-%d/%d", length-1, target.Size) || resp.Header.Get("ETag") != target.ETag || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		r.Error = "identity_or_headers"
		stop()
	}
	if r.Error == "" {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, length+1))
		r.Bytes = int64(len(body))
		r.Hash = fmt.Sprintf("%x", sha256.Sum256(body))
		if total.Add(r.Bytes) > cfSelectionBudget {
			r.Error = "byte_budget"
			stop()
		}
		if readErr != nil {
			r.Error = "body_read"
			r.ErrorDetail = readErr.Error()
		} else if r.Bytes != length {
			r.Error = "incomplete"
		} else {
			for offset := int64(0); offset < length; offset += cfexpBlock {
				if want := reference[offset]; want == "" || want != fmt.Sprintf("%x", sha256.Sum256(body[offset:min(offset+cfexpBlock, length)])) {
					r.Error = "content_mismatch"
					stop()
					break
				}
			}
		}
	}
	r.ElapsedMS = cfexpMS(time.Since(start))
	return r
}

func cfSelectionPick(candidates []cfSelectionCandidate, probes []cfSelectionProbe, dnsOnly bool) string {
	best := ""
	cost := float64(0)
	for _, c := range candidates {
		if dnsOnly && c.Source != "dns" {
			continue
		}
		for _, p := range probes {
			if p.IP == c.IP && p.Kind == "single-8388608" && p.Error == "" && (best == "" || p.ElapsedMS < cost) {
				best = c.IP
				cost = p.ElapsedMS
			}
		}
	}
	return best
}

func TestCFSelectionExperiment(t *testing.T) {
	if *cfexpOut == "" {
		t.Skip("explicit output and Wi-Fi network profile required")
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal("output must be new:", err)
	}
	w := cfSelectionWindow{Schema: 1, Method: "equal-membership-cold-probes-v1", Started: time.Now(), Outcome: "running", Selected: map[string]string{}}
	var total atomic.Int64
	save := func() {
		w.BodyBytes = total.Load()
		if err := cfexpSave(filepath.Join(dir, "selection.json"), w); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		w.Finished = time.Now()
		if w.Outcome == "running" {
			w.Outcome = "failed"
		}
		save()
	}()
	fail := func(reason string) { w.Error = reason; t.Fatal(reason) }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	dns := upstreamnet.NewResolver(network)
	defer dns.Close()
	ordinary := &http.Transport{DialContext: dns.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer ordinary.CloseIdleConnections()
	client := &http.Client{Transport: ordinary, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	targets := map[int]cfexpTarget{}
	for _, song := range []int{1343, 1344, 10552, 10553} {
		target, err := cfexpPrepare(ctx, client, song)
		if err != nil {
			fail(fmt.Sprintf("prepare_%d_%s", song, err))
		}
		targets[song] = target
	}
	if targets[1343].Size > 64<<20 || targets[1344].Size+targets[10552].Size+targets[10553].Size > 200<<20 {
		fail("resource_size_budget")
	}
	if err := cfexpSave(filepath.Join(dir, "targets-private.json"), targets); err != nil {
		fail("save_targets")
	}
	// lookup returns current transaction answers even when they have zero TTL.
	w.DNSIPs, err = dns.ExperimentLookup(ctx, cfexpHost)
	if err != nil || len(w.DNSIPs) == 0 {
		fail("dns_discovery")
	}
	slices.Sort(w.DNSIPs)
	w.Candidates = cfSelectionCandidates(w.DNSIPs)
	if len(w.Candidates) > 14 {
		fail("candidate_budget")
	}
	w.PreparationMS = cfexpMS(time.Since(w.Started))
	save()
	load := func(target cfexpTarget, arm, ip string, round int, ref map[int64]string) cfexpRun {
		if total.Load()+target.Size > cfSelectionBudget {
			fail("byte_budget_before_load")
		}
		tr, closePool, err := cfFamilyTransport(ctx, network, cfexpStatic{[]string{ip}, time.Now().Add(2 * time.Minute)}, target.URL, ip)
		if err != nil {
			fail("fixed_pool_" + err.Error())
		}
		defer closePool()
		r := cfexpLoadBudget(ctx, target, arm, ip, round, tr, ref, &total, cancel, cfSelectionBudget)
		t.Logf("%s song=%d round=%d ip=%s ok=%t elapsed=%.1fms bytes=%d", arm, target.Song, round, ip, r.OK, r.ElapsedMS, r.Bytes)
		return r
	}
	// First DNS answer is an integrity reference, not the selected performance baseline.
	base := load(targets[1343], "reference", w.DNSIPs[0], 0, nil)
	w.Training = append(w.Training, base)
	save()
	if !base.OK {
		fail("reference_failed")
	}
	ref := cfSelectionHashes(base)
	probeStart := time.Now()
	order := slices.Clone(w.Candidates)
	rng := rand.New(rand.NewPCG(21, 20261022))
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	for pass, length := range []int64{1 << 20, 8 << 20} {
		if pass == 1 {
			slices.Reverse(order)
		}
		for _, c := range order {
			if ctx.Err() != nil {
				fail("probe_context")
			}
			p := cfSelectionShort(ctx, network, targets[1343], c.IP, length, ref, &total, cancel)
			w.Probes = append(w.Probes, p)
			save()
			t.Logf("probe bytes=%d ip=%s error=%s time=%.1fms", length, c.IP, p.Error, p.ElapsedMS)
			if err := cfexpPause(ctx, time.Second); err != nil {
				fail("probe_stopped")
			}
		}
	}
	w.ScreenMS = cfexpMS(time.Since(probeStart))
	w.Selected["dns-short"] = cfSelectionPick(w.Candidates, w.Probes, true)
	w.Selected["union-short"] = cfSelectionPick(w.Candidates, w.Probes, false)
	if w.Selected["dns-short"] == "" || w.Selected["union-short"] == "" {
		fail("no_qualified_short_candidate")
	}
	referenceStart := time.Now()
	// Measure every candidate that served correct bytes in either probe, including
	// slow candidates that a ranking filter would discard. Two failed probes are
	// reported as qualification failures, never as poor throughput samples.
	order = slices.DeleteFunc(order, func(c cfSelectionCandidate) bool {
		if slices.ContainsFunc(w.Probes, func(p cfSelectionProbe) bool { return p.IP == c.IP && p.Error == "" }) {
			return false
		}
		w.Unqualified = append(w.Unqualified, c.IP)
		return true
	})
	for round := 1; round <= 2; round++ {
		slices.Reverse(order)
		for _, c := range order {
			if ctx.Err() != nil {
				fail("training_context")
			}
			r := load(targets[1343], "training", c.IP, round, ref)
			w.Training = append(w.Training, r)
			save()
			if err := cfexpPause(ctx, time.Second); err != nil {
				fail("training_stopped")
			}
		}
	}
	w.ReferenceMS = cfexpMS(time.Since(referenceStart))
	bestCost := float64(0)
	for _, c := range w.Candidates {
		var costs []float64
		for _, r := range w.Training {
			if r.Arm == "training" && r.IP == c.IP && r.OK {
				costs = append(costs, r.ElapsedMS)
			}
		}
		if len(costs) == 2 {
			cost := (costs[0] + costs[1]) / 2
			if bestCost == 0 || cost < bestCost {
				bestCost = cost
				w.Selected["union-block"] = c.IP
			}
		}
	}
	if w.Selected["union-block"] == "" {
		fail("no_clean_training_candidate")
	}
	save()
	arms := []string{"dns-short", "union-short", "union-block"}
	for _, song := range cfexpSongs {
		reference := load(targets[song], "integrity", w.Selected["dns-short"], 0, nil)
		w.Evaluation = append(w.Evaluation, reference)
		save()
		if !reference.OK {
			fail("evaluation_reference_failed")
		}
		hashes := cfSelectionHashes(reference)
		for round := 0; round <= 2; round++ {
			sequence := slices.Clone(arms)
			if round > 0 {
				sequence = append(slices.Clone(arms[1:]), arms[0])
			}
			if round == 2 {
				slices.Reverse(sequence)
			}
			for _, arm := range sequence {
				r := load(targets[song], arm, w.Selected[arm], round, hashes)
				w.Evaluation = append(w.Evaluation, r)
				save()
				if err := cfexpPause(ctx, time.Second); err != nil {
					fail("evaluation_stopped")
				}
			}
		}
	}
	if err := network.err(); err != nil {
		fail("network_binding")
	}
	w.Outcome = "completed"
}
