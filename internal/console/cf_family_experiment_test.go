//go:build windows && cf_block_experiment

package console

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

var cfFamilyArms = []string{"auto", "v4", "v6", "extra"}

type cfFamilyCandidate struct {
	IP, Arm     string
	DurationsMS []float64
	Failures    int
	MedianMS    float64
}
type cfFamilyTraining struct {
	Candidates []cfFamilyCandidate
	Runs       []cfexpRun
	Winners    map[string]string
}

func cfFamilyPlan() []cfexpPlanRow {
	var plan []cfexpPlanRow
	for _, song := range cfexpSongs {
		for _, arm := range cfFamilyArms {
			plan = append(plan, cfexpPlanRow{0, song, arm})
		}
	}
	orders := [][]string{{"auto", "v4", "extra", "v6"}, {"v4", "v6", "auto", "extra"}, {"v6", "extra", "v4", "auto"}, {"extra", "auto", "v6", "v4"}}
	for i, arms := range orders {
		for j := range cfexpSongs {
			song := cfexpSongs[(i+j)%len(cfexpSongs)]
			for _, arm := range arms {
				plan = append(plan, cfexpPlanRow{i + 1, song, arm})
			}
		}
	}
	return plan
}

func cfFamilyPick(candidates []cfFamilyCandidate) (map[string]string, error) {
	out := map[string]string{}
	best := map[string]float64{}
	for _, c := range candidates {
		if c.Failures != 0 || len(c.DurationsMS) != 3 {
			continue
		}
		samples := slices.Clone(c.DurationsMS)
		sort.Float64s(samples)
		median := samples[1]
		if _, ok := best[c.Arm]; !ok || median < best[c.Arm] {
			best[c.Arm] = median
			out[c.Arm] = c.IP
		}
	}
	for _, arm := range []string{"v4", "v6", "extra"} {
		if out[arm] == "" {
			return out, errors.New("no_three_success_candidate_" + arm)
		}
	}
	return out, nil
}

func cfFamilyTransport(ctx context.Context, dns upstreamrequest.DNSPool, target, ip string) (http.RoundTripper, func(), error) {
	pool, err := upstreamrequest.NewPool("direct", dns, nil)
	if err != nil {
		return nil, func() {}, err
	}
	candidates, err := pool.Candidates(ctx, target)
	if err == nil {
		for _, c := range candidates {
			if c.IP == ip {
				return c.Transport, pool.Close, nil
			}
		}
	}
	pool.Close()
	return nil, func() {}, errors.New("chosen_candidate_unavailable")
}

func cfFamilyCritical(r cfexpRun) bool {
	for _, q := range r.Requests {
		if q.Status == 429 || q.Status == 503 || q.Error == "content_mismatch" {
			return true
		}
	}
	return false
}

func TestCFFamilyExperiment(t *testing.T) {
	if *cfexpOut == "" {
		t.Skip("explicit -cfexp-out required for live experiment")
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal("output must be new:", err)
	}
	window := cfexpWindow{Schema: 2, Started: time.Now(), Outcome: "running", Extras: cfexpExtras, Songs: cfexpSongs, Selected: map[string]string{}, Plan: cfFamilyPlan()}
	training := cfFamilyTraining{Winners: map[string]string{}}
	save := func() {
		if err := cfexpSave(filepath.Join(dir, "window.json"), window); err != nil {
			t.Fatal(err)
		}
		if err := cfexpSave(filepath.Join(dir, "training.json"), training); err != nil {
			t.Fatal(err)
		}
	}
	save()
	defer func() {
		window.Finished = time.Now()
		if window.Outcome == "running" {
			window.Outcome = "failed"
		}
		save()
	}()
	fail := func(reason string) { window.Error = reason; t.Fatal(reason) }
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	dns := &directDNS{}
	defer dns.close()
	ordinary := &http.Transport{DialContext: dns.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer ordinary.CloseIdleConnections()
	client := &http.Client{Transport: ordinary, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	targets := map[int]cfexpTarget{}
	var size int64
	for _, song := range append(slices.Clone(cfexpSongs), 1343) {
		target, err := cfexpPrepare(ctx, client, song)
		if err != nil {
			fail(fmt.Sprintf("prepare_%d_%s", song, err))
		}
		targets[song] = target
		if song != 1343 {
			size += target.Size
		}
		t.Logf("prepared song=%d size=%d", song, target.Size)
	}
	if targets[1343].Size > 64<<20 {
		fail("training_size_budget")
	}
	if err := cfexpSave(filepath.Join(dir, "targets-private.json"), targets); err != nil {
		fail("save_targets")
	}
	window.DNS, err = dns.Addresses(ctx, cfexpHost)
	if err != nil {
		fail("dns_discovery")
	}
	baseline, err := upstreamrequest.NewPool("direct", dns, nil)
	if err != nil {
		fail("dns_pool")
	}
	defer baseline.Close()
	explicit := cfexpStatic{ips: cfexpExtras, until: time.Now().Add(45 * time.Minute)}
	extra, err := upstreamrequest.NewPool("direct", explicit, nil)
	if err != nil {
		fail("extra_pool")
	}
	defer extra.Close()
	monitors := map[string]*upstreamstate.Monitor{}
	for arm, pool := range map[string]*upstreamrequest.Pool{"dns": baseline, "extra": extra} {
		channel := upstreamrequest.NewChannel()
		channel.Publish(&cfexpProvider{api: baseline, resource: pool, ordinary: ordinary})
		m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel, PlaybackSongCandidates: func(context.Context) []int64 { return []int64{1343} }})
		if err != nil {
			fail("monitor_init")
		}
		monitors[arm] = m
		defer m.Close()
	}
	check := func(arm string, round int) {
		t.Logf("monitor arm=%s round=%d", arm, round)
		if err := monitors[arm].CheckSelected(ctx, upstreamstate.CheckThroughput); err != nil {
			fail("monitor_check")
		}
		if err := cfexpSave(filepath.Join(dir, fmt.Sprintf("monitor-%s-%d-private.json", arm, round)), monitors[arm].Snapshot()); err != nil {
			fail("save_monitor")
		}
	}
	for _, arm := range []string{"dns", "extra"} {
		check(arm, 1)
		for _, r := range monitors[arm].Results(upstreamstate.Resource) {
			if r.State != "available" || r.EstimatedSpeedBPS == nil {
				continue
			}
			family := arm
			if arm == "dns" {
				family = "v6"
				if net.ParseIP(r.IP).To4() != nil {
					family = "v4"
				}
			}
			training.Candidates = append(training.Candidates, cfFamilyCandidate{IP: r.IP, Arm: family})
		}
	}
	sort.Slice(training.Candidates, func(i, j int) bool { return training.Candidates[i].IP < training.Candidates[j].IP })
	if 20*size+3*int64(len(training.Candidates))*targets[1343].Size+640<<20 > 6<<30 {
		fail("planned_byte_budget")
	}
	save()
	var total atomic.Int64
	var reference map[int64]string
	for round := 1; round <= 3; round++ {
		order := make([]int, len(training.Candidates))
		for i := range order {
			order[i] = (i + (round-1)*max(1, len(order)/3)) % len(order)
		}
		if round == 2 {
			slices.Reverse(order)
		}
		for _, index := range order {
			candidate := &training.Candidates[index]
			var source upstreamrequest.DNSPool = dns
			if candidate.Arm == "extra" {
				source = explicit
			}
			tr, closePool, err := cfFamilyTransport(ctx, source, targets[1343].URL, candidate.IP)
			if err != nil {
				fail("training_candidate_removed")
			}
			// A bad training path may stop itself; only critical failures stop the batch.
			attempt, stop := context.WithCancel(ctx)
			run := cfexpLoad(attempt, targets[1343], candidate.Arm, candidate.IP, round, tr, reference, &total, stop)
			stop()
			closePool()
			training.Runs = append(training.Runs, run)
			if run.OK {
				candidate.DurationsMS = append(candidate.DurationsMS, run.ElapsedMS)
				if reference == nil {
					reference = map[int64]string{}
					for _, q := range run.Requests {
						reference[q.Offset] = q.Hash
					}
				}
			} else {
				candidate.Failures++
			}
			save()
			t.Logf("training round=%d family=%s ip=%s ok=%v elapsed=%.0fms", round, candidate.Arm, candidate.IP, run.OK, run.ElapsedMS)
			if cfFamilyCritical(run) {
				fail("critical_training_failure")
			}
			if total.Load() > 6<<30 {
				fail("byte_budget")
			}
			if err := cfexpPause(ctx, 2*time.Second); err != nil {
				fail("training_context")
			}
		}
	}
	for i := range training.Candidates {
		c := &training.Candidates[i]
		if len(c.DurationsMS) == 3 {
			samples := slices.Clone(c.DurationsMS)
			sort.Float64s(samples)
			c.MedianMS = samples[1]
		}
	}
	training.Winners, err = cfFamilyPick(training.Candidates)
	save()
	if err != nil {
		fail(err.Error())
	}
	check("dns", 2)
	check("dns", 3)
	ss := monitors["dns"].Selections(upstreamstate.Resource, upstreamstate.Constraints{Target: "https://" + cfexpHost})
	if len(ss) == 0 {
		fail("no_auto_recommendation")
	}
	window.Selected["auto"] = ss[0].Channel.IP
	for arm, ip := range training.Winners {
		window.Selected[arm] = ip
	}
	save()
	t.Logf("fixed selections auto=%s v4=%s v6=%s extra=%s", window.Selected["auto"], window.Selected["v4"], window.Selected["v6"], window.Selected["extra"])
	refs := map[int]map[int64]string{}
	failures := 0
	for _, p := range window.Plan {
		if ctx.Err() != nil {
			fail("window_context")
		}
		target := targets[p.Song]
		ip := window.Selected[p.Arm]
		var source upstreamrequest.DNSPool = dns
		if p.Arm == "extra" {
			source = explicit
		}
		tr, closePool, err := cfFamilyTransport(ctx, source, target.URL, ip)
		if err != nil {
			fail("test_candidate_removed")
		}
		run := cfexpLoad(ctx, target, p.Arm, ip, p.Round, tr, refs[p.Song], &total, cancel)
		closePool()
		window.Runs = append(window.Runs, run)
		save()
		t.Logf("run round=%d song=%d arm=%s ip=%s ok=%v elapsed=%.0fms peak=%d", p.Round, p.Song, p.Arm, ip, run.OK, run.ElapsedMS, run.PeakActive)
		if p.Round == 0 && p.Arm == "auto" && run.OK {
			ref := map[int64]string{}
			for _, q := range run.Requests {
				ref[q.Offset] = q.Hash
			}
			refs[p.Song] = ref
		}
		if run.OK {
			failures = 0
		} else {
			failures++
		}
		if p.Round == 0 && !run.OK {
			fail("warmup_failed")
		}
		if failures >= 2 {
			fail("consecutive_failures")
		}
		if err := cfexpPause(ctx, 2*time.Second); err != nil {
			fail("window_stopped")
		}
	}
	window.Outcome = "complete"
}

func TestCFFamilyHarness(t *testing.T) {
	plan := cfFamilyPlan()
	if len(plan) != 60 {
		t.Fatal("plan length")
	}
	for _, song := range cfexpSongs {
		positions := map[string][4]int{}
		edges := map[string]int{}
		for round := 1; round <= 4; round++ {
			var arms []string
			for _, r := range plan {
				if r.Song == song && r.Round == round {
					arms = append(arms, r.Arm)
				}
			}
			if len(arms) != 4 {
				t.Fatal("missing arm")
			}
			for i, a := range arms {
				p := positions[a]
				p[i]++
				positions[a] = p
				if i > 0 {
					edges[arms[i-1]+"/"+a]++
				}
			}
		}
		if len(edges) != 12 {
			t.Fatal("unbalanced preceding arms")
		}
		for _, n := range edges {
			if n != 1 {
				t.Fatal("duplicate preceding arm")
			}
		}
		for _, p := range positions {
			if p != [4]int{1, 1, 1, 1} {
				t.Fatal("unbalanced position")
			}
		}
	}
	candidates := []cfFamilyCandidate{{IP: "slower4", Arm: "v4", DurationsMS: []float64{100, 10, 100}}, {IP: "fast4", Arm: "v4", DurationsMS: []float64{20, 30, 25}}, {IP: "failed6", Arm: "v6", DurationsMS: []float64{1, 1, 1}, Failures: 1}, {IP: "good6", Arm: "v6", DurationsMS: []float64{40, 30, 50}}, {IP: "goodExtra", Arm: "extra", DurationsMS: []float64{20, 30, 40}}}
	winner, err := cfFamilyPick(candidates)
	if err != nil || winner["v4"] != "fast4" || winner["v6"] != "good6" {
		t.Fatal("median/reliability selection", winner, err)
	}
	_, err = cfFamilyPick(candidates[:3])
	if err == nil {
		t.Fatal("missing family must block")
	}
}
