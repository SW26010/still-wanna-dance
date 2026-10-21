//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
	"still-wanna-dance/internal/videometa"
)

var cfexpOut = flag.String("cfexp-out", "", "new private experiment output directory; empty disables live requests")

const cfexpHost = "play.udon.dance"
const cfexpBlock int64 = 1 << 20

var cfexpExtras = []string{"103.21.244.1", "104.16.0.1", "104.24.0.1", "162.158.0.1"}
var cfexpSongs = []int{1344, 10552, 10553}

type cfexpTarget struct {
	Song            int
	URL, Host, ETag string
	Size            int64
}
type cfexpPlanRow struct {
	Round, Song int
	Arm         string
}
type cfexpRequest struct {
	Offset, Length, Bytes                      int64
	StartMS, HeadersMS, EndMS                  float64
	Status                                     int
	Remote, Protocol, Cache, Colo, Hash, Error string
	Reused                                     bool
}
type cfexpRun struct {
	Round, Song                               int
	Arm, IP                                   string
	Started                                   time.Time
	Size, Bytes                               int64
	ElapsedMS, CPUMS, FirstBlockMS, Prefix8MS float64
	PeakActive, PeakOpen                      int
	OK                                        bool
	Error                                     string
	Requests                                  []cfexpRequest
}
type cfexpWindow struct {
	Schema            int
	Started, Finished time.Time
	Outcome, Error    string
	DNS               []upstreamrequest.Address
	Extras            []string
	Songs             []int
	Selected          map[string]string
	Plan              []cfexpPlanRow
	Runs              []cfexpRun
}

func cfexpSave(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
func cfexpMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func cfexpCPU() time.Duration {
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user) != nil {
		return 0
	}
	return time.Duration((uint64(kernel.HighDateTime)<<32|uint64(kernel.LowDateTime))+(uint64(user.HighDateTime)<<32|uint64(user.LowDateTime))) * 100 * time.Nanosecond
}
func cfexpPause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Explicit experimental membership only, never installed in application DNS.
type cfexpStatic struct {
	ips   []string
	until time.Time
}

func (d cfexpStatic) Addresses(context.Context, string) ([]upstreamrequest.Address, error) {
	return d.CurrentAddresses(cfexpHost), nil
}
func (d cfexpStatic) CurrentAddresses(host string) []upstreamrequest.Address {
	if host != cfexpHost || !time.Now().Before(d.until) {
		return nil
	}
	var out []upstreamrequest.Address
	for _, ip := range d.ips {
		out = append(out, upstreamrequest.Address{IP: ip, Expires: d.until})
	}
	return out
}
func (cfexpStatic) DNSChanged() <-chan struct{} { return nil }

// Use the real monitor while limiting this experiment to the CF route.
type cfexpProvider struct {
	api, resource *upstreamrequest.Pool
	ordinary      http.RoundTripper
}

func (p *cfexpProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	return p.ordinary.RoundTrip(r)
}
func (p *cfexpProvider) pool(target string) *upstreamrequest.Pool {
	u, err := url.Parse(target)
	if err != nil {
		return nil
	}
	if u.Hostname() == cfexpHost {
		return p.resource
	}
	if u.Hostname() == "api.udon.dance" && (u.Path == "/Api/Songs/list" || u.Query().Get("node") == "cf") {
		return p.api
	}
	return nil
}
func (p *cfexpProvider) Candidates(ctx context.Context, target string) ([]upstreamrequest.Candidate, error) {
	q := p.pool(target)
	if q == nil {
		return nil, nil
	}
	return q.Candidates(ctx, target)
}
func (p *cfexpProvider) Current(target string) []upstreamrequest.Candidate {
	q := p.pool(target)
	if q == nil {
		return nil
	}
	return q.Current(target)
}
func (p *cfexpProvider) Changed() <-chan struct{} { return p.api.Changed() }

func cfexpPrepare(ctx context.Context, client *http.Client, song int) (cfexpTarget, error) {
	target := cfexpTarget{Song: song}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.udon.dance/Api/Songs/play?node=cf&id=%d", song), nil)
	resp, err := client.Do(req)
	if err != nil {
		return target, errors.New("prepare_transport")
	}
	resp.Body.Close()
	if resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 307 && resp.StatusCode != 308 {
		return target, fmt.Errorf("prepare_status_%d", resp.StatusCode)
	}
	u, err := resp.Location()
	if err != nil {
		return target, errors.New("prepare_location")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() != cfexpHost || u.User != nil || u.Fragment != "" {
		return target, errors.New("prepare_host")
	}
	meta, err := videometa.Parse(u, 256<<20)
	if err != nil || meta.Size < 16<<20 {
		return target, errors.New("prepare_size_or_metadata")
	}
	u.Scheme = "https"
	target.URL = u.String()
	target.Host = u.Hostname()
	target.Size = meta.Size
	req, _ = http.NewRequestWithContext(ctx, "GET", target.URL, nil)
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err = client.Do(req)
	if err != nil {
		return target, errors.New("preflight_transport")
	}
	defer resp.Body.Close()
	target.ETag = resp.Header.Get("ETag")
	if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-0/%d", target.Size) || len(target.ETag) < 2 || strings.HasPrefix(target.ETag, "W/") {
		return target, errors.New("preflight_response")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2))
	if err != nil || len(b) != 1 {
		return target, errors.New("preflight_body")
	}
	return target, nil
}

type cfexpCapture struct {
	mu            sync.Mutex
	tr            http.RoundTripper
	target        cfexpTarget
	ip            string
	started       time.Time
	reference     map[int64]string
	rows          []cfexpRequest
	completed     map[int64]bool
	first, prefix float64
	total         *atomic.Int64
	cancel        context.CancelFunc
}

func (c *cfexpCapture) add(row cfexpRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, row)
	if row.Error != "" {
		return
	}
	c.completed[row.Offset] = true
	if row.Offset == 0 && c.first == 0 {
		c.first = row.EndMS
	}
	if c.prefix == 0 {
		ready := true
		for i := int64(0); i < min(8*cfexpBlock, c.target.Size); i += cfexpBlock {
			ready = ready && c.completed[i]
		}
		if ready {
			c.prefix = row.EndMS
		}
	}
}
func (c *cfexpCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	row := cfexpRequest{StartMS: cfexpMS(time.Since(c.started))}
	var end int64
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &row.Offset, &end); err != nil {
		return nil, errors.New("missing_range")
	}
	row.Length = end - row.Offset + 1
	if row.Offset < 0 || row.Offset%cfexpBlock != 0 || row.Length != min(cfexpBlock, c.target.Size-row.Offset) {
		return nil, errors.New("unexpected_range")
	}
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		row.Remote, _, _ = net.SplitHostPort(info.Conn.RemoteAddr().String())
		row.Reused = info.Reused
	}}
	resp, err := c.tr.RoundTrip(r.Clone(httptrace.WithClientTrace(r.Context(), trace)))
	row.HeadersMS = cfexpMS(time.Since(c.started))
	if err != nil {
		row.EndMS = row.HeadersMS
		row.Error = "transport"
		if r.Context().Err() != nil {
			row.Error = "context"
		}
		c.add(row)
		return nil, errors.New(row.Error)
	}
	row.Status, row.Protocol, row.Cache = resp.StatusCode, resp.Proto, resp.Header.Get("CF-Cache-Status")
	ray := resp.Header.Get("CF-Ray")
	if i := strings.LastIndexByte(ray, '-'); i >= 0 {
		row.Colo = ray[i+1:]
	}
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		row.Error = "stop_status"
		c.cancel()
	}
	if row.Error == "" && (resp.StatusCode != 206 || row.Remote != c.ip || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", row.Offset, end, c.target.Size) || resp.ContentLength != row.Length || resp.Header.Get("ETag") != c.target.ETag || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity")) {
		row.Error = "identity_or_headers"
		c.cancel()
	}
	if row.Error != "" {
		resp.Body.Close()
		row.EndMS = cfexpMS(time.Since(c.started))
		c.add(row)
		return nil, errors.New(row.Error)
	}
	resp.Body = &cfexpBody{ReadCloser: resp.Body, c: c, row: row, h: sha256.New()}
	return resp, nil
}

type cfexpBody struct {
	io.ReadCloser
	readMu sync.Mutex
	c      *cfexpCapture
	row    cfexpRequest
	h      hash.Hash
	once   sync.Once
}

func (b *cfexpBody) Read(p []byte) (int, error) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	n, err := b.ReadCloser.Read(p)
	b.row.Bytes += int64(n)
	b.h.Write(p[:n])
	if b.c.total.Add(int64(n)) > 6<<30 {
		b.row.Error = "byte_budget"
		b.c.cancel()
	}
	if err != nil && err != io.EOF {
		b.row.Error = "body_read"
	}
	return n, err
}
func (b *cfexpBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.readMu.Lock()
		defer b.readMu.Unlock()
		b.row.EndMS = cfexpMS(time.Since(b.c.started))
		b.row.Hash = hex.EncodeToString(b.h.Sum(nil))
		if b.row.Error == "" && b.row.Bytes != b.row.Length {
			b.row.Error = "incomplete"
		}
		if want, ok := b.c.reference[b.row.Offset]; ok && b.row.Error == "" && want != b.row.Hash {
			b.row.Error = "content_mismatch"
			b.c.cancel()
		}
		b.c.add(b.row)
	})
	return err
}

func cfexpLoad(ctx context.Context, target cfexpTarget, arm, ip string, round int, tr http.RoundTripper, reference map[int64]string, total *atomic.Int64, stop context.CancelFunc) cfexpRun {
	r := cfexpRun{Round: round, Song: target.Song, Arm: arm, IP: ip, Size: target.Size, Started: time.Now()}
	capture := &cfexpCapture{tr: tr, target: target, ip: ip, reference: reference, completed: map[int64]bool{}, total: total, cancel: stop}
	var mu sync.Mutex
	loader, err := cacheproxy.NewDiscardLoader(cacheproxy.DiscardConfig{Transport: capture, Observe: func(e blocksched.Event) {
		mu.Lock()
		r.PeakActive = max(r.PeakActive, e.Active)
		r.PeakOpen = max(r.PeakOpen, e.Open)
		mu.Unlock()
	}})
	if err != nil {
		r.Error = "loader_init"
		return r
	}
	attempt, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cpu := cfexpCPU()
	capture.started = time.Now()
	err = loader.Load(attempt, target.URL)
	r.ElapsedMS = cfexpMS(time.Since(capture.started))
	r.CPUMS = cfexpMS(cfexpCPU() - cpu)
	loader.Close()
	r.Requests = capture.rows
	r.FirstBlockMS = capture.first
	r.Prefix8MS = capture.prefix
	if err != nil {
		r.Error = "load_failed"
		if attempt.Err() != nil {
			r.Error = "timeout_or_canceled"
		}
	}
	for _, row := range r.Requests {
		r.Bytes += row.Bytes
		if row.Error != "" && r.Error == "" {
			r.Error = "request_failed"
		}
	}
	if len(capture.completed) != int((target.Size+cfexpBlock-1)/cfexpBlock) && r.Error == "" {
		r.Error = "coverage"
	}
	r.OK = r.Error == ""
	return r
}

func cfexpPlan() []cfexpPlanRow {
	var out []cfexpPlanRow
	for _, song := range cfexpSongs {
		for _, arm := range []string{"dns", "extra"} {
			out = append(out, cfexpPlanRow{0, song, arm})
		}
	}
	for round := 1; round <= 4; round++ {
		for j := range cfexpSongs {
			song := cfexpSongs[(j+round-1)%len(cfexpSongs)]
			arms := []string{"dns", "extra"}
			if round%2 == 0 {
				slices.Reverse(arms)
			}
			for _, arm := range arms {
				out = append(out, cfexpPlanRow{round, song, arm})
			}
		}
	}
	return out
}

func TestCFBlockExperiment(t *testing.T) {
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
	window := cfexpWindow{Schema: 1, Started: time.Now(), Outcome: "running", Extras: cfexpExtras, Songs: cfexpSongs, Selected: map[string]string{}, Plan: cfexpPlan()}
	save := func() {
		if err := cfexpSave(filepath.Join(dir, "window.json"), window); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	dns := &directDNS{dialer: network}
	defer dns.close()
	ordinary := &http.Transport{DialContext: dns.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer ordinary.CloseIdleConnections()
	client := &http.Client{Transport: ordinary, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	targets := map[int]cfexpTarget{}
	var size int64
	for _, song := range cfexpSongs {
		target, err := cfexpPrepare(ctx, client, song)
		if err != nil {
			fail(fmt.Sprintf("prepare_%d_%s", song, err))
		}
		targets[song] = target
		size += target.Size
		t.Logf("prepared song=%d size=%d", song, target.Size)
	}
	if size > 400<<20 {
		fail("resource_budget")
	}
	if err := cfexpSave(filepath.Join(dir, "targets-private.json"), targets); err != nil {
		fail("save_targets")
	}
	window.DNS, err = dns.Addresses(ctx, cfexpHost)
	if err != nil {
		fail("dns_discovery")
	}
	save()
	baseline, err := network.pool(dns)
	if err != nil {
		fail("baseline_pool")
	}
	defer baseline.Close()
	explicit := cfexpStatic{ips: cfexpExtras, until: time.Now().Add(35 * time.Minute)}
	extra, err := network.pool(explicit)
	if err != nil {
		fail("extra_pool")
	}
	defer extra.Close()
	monitors := map[string]*upstreamstate.Monitor{}
	for arm, pool := range map[string]*upstreamrequest.Pool{"dns": baseline, "extra": extra} {
		ch := upstreamrequest.NewChannel()
		ch.Publish(&cfexpProvider{api: baseline, resource: pool, ordinary: ordinary})
		m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: ch, PlaybackSongCandidates: func(context.Context) []int64 { return []int64{1343} }})
		if err != nil {
			fail("monitor_init")
		}
		defer m.Close()
		monitors[arm] = m
	}
	for round := 1; round <= 3; round++ {
		arms := []string{"dns", "extra"}
		if round%2 == 0 {
			slices.Reverse(arms)
		}
		for _, arm := range arms {
			t.Logf("training round=%d arm=%s", round, arm)
			if err := monitors[arm].CheckSelected(ctx, upstreamstate.CheckThroughput); err != nil {
				fail("monitor_check")
			}
			if err := cfexpSave(filepath.Join(dir, fmt.Sprintf("monitor-%s-%d-private.json", arm, round)), monitors[arm].Snapshot()); err != nil {
				fail("save_monitor")
			}
			ss := monitors[arm].Selections(upstreamstate.Resource, upstreamstate.Constraints{Target: "https://" + cfexpHost})
			if len(ss) == 0 {
				fail("no_recommendation_" + arm)
			}
			window.Selected[arm] = ss[0].Channel.IP
			save()
			t.Logf("recommended arm=%s ip=%s", arm, ss[0].Channel.IP)
		}
	}
	references := map[int]map[int64]string{}
	var total atomic.Int64
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
		pool, err := network.pool(source)
		if err != nil {
			fail("run_pool")
		}
		cs, err := pool.Candidates(ctx, target.URL)
		if err != nil {
			pool.Close()
			fail("run_candidates")
		}
		var tr http.RoundTripper
		for _, c := range cs {
			if c.IP == ip {
				tr = c.Transport
				break
			}
		}
		if tr == nil {
			pool.Close()
			fail("chosen_candidate_removed")
		}
		result := cfexpLoad(ctx, target, p.Arm, ip, p.Round, tr, references[p.Song], &total, cancel)
		pool.Close()
		window.Runs = append(window.Runs, result)
		save()
		t.Logf("run round=%d song=%d arm=%s ip=%s ok=%v elapsed=%.0fms peak=%d", p.Round, p.Song, p.Arm, ip, result.OK, result.ElapsedMS, result.PeakActive)
		if p.Round == 0 && p.Arm == "dns" && result.OK {
			ref := map[int64]string{}
			for _, row := range result.Requests {
				ref[row.Offset] = row.Hash
			}
			references[p.Song] = ref
		}
		if result.OK {
			failures = 0
		} else {
			failures++
		}
		if p.Round == 0 && !result.OK {
			fail("warmup_failed")
		}
		if failures >= 2 {
			fail("consecutive_failures")
		}
		if err := cfexpPause(ctx, 2*time.Second); err != nil {
			fail("window_stopped")
		}
	}
	if err := network.err(); err != nil {
		fail("network_binding_failed")
	}
	window.Outcome = "complete"
}

type cfexpFunc func(*http.Request) (*http.Response, error)

func (f cfexpFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCFBlockHarness(t *testing.T) {
	const size int64 = 3*cfexpBlock + 123
	target := cfexpTarget{Song: 1, Host: cfexpHost, Size: size, ETag: "\"fixture\"", URL: fmt.Sprintf("https://%s/files/123/42-fixture.mp4?e=11111111111111111111111111111111&s=%d", cfexpHost, size)}
	transport := cfexpFunc(func(r *http.Request) (*http.Response, error) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if trace := httptrace.ContextClientTrace(r.Context()); trace != nil && trace.GotConn != nil {
			trace.GotConn(httptrace.GotConnInfo{Conn: cfexpConn{}})
		}
		return &http.Response{StatusCode: 206, Proto: "HTTP/1.1", Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, size)}, "Etag": {target.ETag}}, ContentLength: end - start + 1, Body: io.NopCloser(io.LimitReader(cfexpZeros{}, end-start+1)), Request: r}, nil
	})
	var total atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := cfexpLoad(ctx, target, "dns", "127.0.0.1", 0, transport, nil, &total, cancel)
	if !first.OK || first.Bytes != size || len(first.Requests) != 4 || first.Prefix8MS <= 0 {
		t.Fatalf("bad product load: %+v", first)
	}
	ref := map[int64]string{}
	for _, r := range first.Requests {
		ref[r.Offset] = r.Hash
	}
	ref[0] = "deliberately_wrong"
	second := cfexpLoad(ctx, target, "extra", "127.0.0.1", 1, transport, ref, &total, cancel)
	if second.OK || ctx.Err() == nil {
		t.Fatal("content mismatch must reject and stop")
	}
	plan := cfexpPlan()
	if len(plan) != 30 {
		t.Fatal("plan length")
	}
	for _, song := range cfexpSongs {
		before := 0
		for i, r := range plan {
			if r.Round > 0 && r.Song == song && r.Arm == "dns" && i+1 < len(plan) && plan[i+1].Song == song && plan[i+1].Arm == "extra" {
				before++
			}
		}
		if before != 2 {
			t.Fatal("unbalanced pair order")
		}
	}
}

type cfexpZeros struct{}

func (cfexpZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type cfexpConn struct{ net.Conn }

func (cfexpConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 443} }
