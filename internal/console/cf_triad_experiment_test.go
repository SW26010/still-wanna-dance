//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamnet"
)

var cfTriadCandidates = flag.String("cftriad-candidates", "", "frozen public candidate JSON")
var cfTriadWindow = flag.Int("cftriad-window", 1, "pre-registered window 1 or 2")
var cfTriadPilotOnly = flag.Bool("cftriad-pilot-only", false, "run pilot only; separate diagnostic, not full-pool evidence")

type cfTriadPools struct {
	Method string
	Pools  map[string][]string
	Pilot  []string
}

type cfTriadCandidate struct{ IP, Pool string }
type cfTriadProgress struct {
	Bytes int64
	MS    float64
}
type cfTriadRow struct {
	Stage, Pool, IP, Method                                               string
	Song, Round, Position                                                 int
	Started                                                               time.Time
	Requested, Bytes                                                      int64
	Status                                                                int
	Error, ErrorDetail, Hash, Remote, Local, Host, SNI, Protocol          string
	Range, ContentRange, ContentEncoding, ETag, Cache, Age, Ray, Location string
	ContentLength                                                         int64
	TLSVerified, Reused                                                   bool
	TCPMS, TLSMS, FirstByteMS, HeadersMS, BodyMS, ElapsedMS               float64
	Progress                                                              []cfTriadProgress
}
type cfTriadResult struct {
	Schema                                   int
	Method, CandidatesSHA256, Outcome, Error string
	Window                                   int
	PilotOnly                                bool
	Started, Finished                        time.Time
	Candidates, Pilot                        []cfTriadCandidate
	DNSIPs                                   []string
	Rows                                     []cfTriadRow
	BodyBytes                                int64
}

func cfTriadOrder(i int) []string {
	return [][]string{{"speed", "http", "https"}, {"speed", "https", "http"}, {"http", "https", "speed"}, {"https", "http", "speed"}}[i%4]
}

func cfTriadPilotOrder(position, round, window int) []string {
	// Use the candidate's stable position, not its rotated execution index.
	return cfTriadOrder(position + round + 2*(window-1))
}

func cfTriadPlan(p cfTriadPools, dns []string, window int) ([]cfTriadCandidate, error) {
	if p.Method != "cf-triad-v1" || (window != 1 && window != 2) {
		return nil, fmt.Errorf("invalid method/window")
	}
	keys := []string{"A", "B_uniform", "B_stratified"}
	rng := rand.New(rand.NewPCG(202610220421, 21))
	shuffled := map[string][]string{}
	for _, key := range keys {
		ips := append([]string(nil), p.Pools[key]...)
		if len(ips) != 104 {
			return nil, fmt.Errorf("pool %s must contain 104 IPs", key)
		}
		seen := map[string]bool{}
		for _, ip := range ips {
			a := net.ParseIP(ip)
			if a == nil || a.To4() == nil || !a.IsGlobalUnicast() || a.IsPrivate() || seen[ip] {
				return nil, fmt.Errorf("invalid pool IP")
			}
			seen[ip] = true
		}
		rng.Shuffle(len(ips), func(i, j int) { ips[i], ips[j] = ips[j], ips[i] })
		shuffled[key] = ips
	}
	var out []cfTriadCandidate
	for i := 0; i < 104; i++ {
		for k := 0; k < 3; k++ {
			key := keys[(k+i)%3]
			out = append(out, cfTriadCandidate{shuffled[key][i], key})
		}
	}
	if window == 2 {
		slices.Reverse(out)
	}
	// DNS is a separate small baseline. The empty IP uses current ordinary DNS.
	base := append([]string{""}, dns...)
	for i := len(base) - 1; i >= 0; i-- {
		at := i * len(out) / len(base)
		out = slices.Insert(out, at, cfTriadCandidate{base[i], "C"})
	}
	return out, nil
}

// A fresh HTTP/1.1 transport for every observation, with unchanged Host/SNI.
// Public HTTP exists only in this explicitly invoked experiment.
func cfTriadTransport(dial func(context.Context, string, string) (net.Conn, error), ip string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if ip != "" {
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				address = net.JoinHostPort(ip, port)
			}
			return dial(ctx, network, address)
		},
		DisableCompression: true, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

func cfTriadRead(parent context.Context, tr http.RoundTripper, target cfexpTarget, method, ip string, length int64, hash string, timeout time.Duration) (row cfTriadRow, body []byte) {
	row = cfTriadRow{IP: ip, Method: method, Song: target.Song, Requested: length, Started: time.Now()}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	u, _ := url.Parse(target.URL)
	if method == "speed" {
		u, _ = url.Parse(fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", length))
	} else {
		u.Scheme = method
	}
	row.Host = u.Hostname()
	var mu sync.Mutex
	// httptrace permits callbacks after RoundTrip returns. Keep their state
	// separate from the returned observation and snapshot it at headers/error.
	observed := cfTriadRow{Started: row.Started}
	var tcpStart, tlsStart time.Time
	trace := &httptrace.ClientTrace{
		ConnectStart: func(_, _ string) { mu.Lock(); defer mu.Unlock(); tcpStart = time.Now() },
		ConnectDone: func(_, _ string, _ error) {
			mu.Lock()
			defer mu.Unlock()
			observed.TCPMS = cfexpMS(time.Since(tcpStart))
		},
		TLSHandshakeStart: func() { mu.Lock(); defer mu.Unlock(); tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			mu.Lock()
			defer mu.Unlock()
			observed.TLSMS = cfexpMS(time.Since(tlsStart))
		},
		GotFirstResponseByte: func() { mu.Lock(); defer mu.Unlock(); observed.FirstByteMS = cfexpMS(time.Since(observed.Started)) },
		GotConn: func(c httptrace.GotConnInfo) {
			mu.Lock()
			defer mu.Unlock()
			observed.Remote, _, _ = net.SplitHostPort(c.Conn.RemoteAddr().String())
			observed.Local = c.Conn.LocalAddr().String()
			observed.Reused = c.Reused
		},
	}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", u.String(), nil)
	req.Header.Set("Accept-Encoding", "identity")
	if method != "speed" {
		row.Range = fmt.Sprintf("bytes=0-%d", length-1)
		req.Header.Set("Range", row.Range)
	}
	resp, err := tr.RoundTrip(req)
	mu.Lock()
	row.TCPMS, row.TLSMS, row.FirstByteMS = observed.TCPMS, observed.TLSMS, observed.FirstByteMS
	row.Remote, row.Local, row.Reused = observed.Remote, observed.Local, observed.Reused
	mu.Unlock()
	row.HeadersMS = cfexpMS(time.Since(row.Started))
	defer func() { row.ElapsedMS = cfexpMS(time.Since(row.Started)) }()
	if err != nil {
		row.Error = "transport"
		row.ErrorDetail = err.Error()
		return
	}
	defer resp.Body.Close()
	row.Status = resp.StatusCode
	row.Protocol = resp.Proto
	row.ContentLength = resp.ContentLength
	row.ContentRange = resp.Header.Get("Content-Range")
	row.ContentEncoding = resp.Header.Get("Content-Encoding")
	row.ETag = resp.Header.Get("ETag")
	row.Cache = resp.Header.Get("CF-Cache-Status")
	row.Age = resp.Header.Get("Age")
	row.Ray = resp.Header.Get("CF-Ray")
	row.Location = resp.Header.Get("Location")
	if resp.TLS != nil {
		row.TLSVerified = len(resp.TLS.VerifiedChains) > 0
		row.SNI = resp.TLS.ServerName
	}
	if row.Status == 429 || row.Status == 503 {
		row.Error = "stop_status"
		return
	}
	if row.Status >= 300 && row.Status < 400 {
		row.Error = "redirect"
		return
	}
	wantStatus := 206
	if method == "speed" {
		wantStatus = 200
	}
	if row.Status != wantStatus {
		row.Error = "status"
		return
	}
	if (ip != "" && row.Remote != ip) || (u.Scheme == "https" && (!row.TLSVerified || row.SNI != u.Hostname())) || row.Protocol != "HTTP/1.1" || row.Reused {
		row.Error = "identity"
		return
	}
	if row.ContentLength != length || (row.ContentEncoding != "" && row.ContentEncoding != "identity") {
		row.Error = "headers"
		return
	}
	if method != "speed" && (row.ContentRange != fmt.Sprintf("bytes 0-%d/%d", length-1, target.Size) || row.ETag != target.ETag) {
		row.Error = "headers"
		return
	}
	bodyStart := time.Now()
	reader := io.LimitReader(resp.Body, length+1)
	buf := make([]byte, 32<<10)
	next := int64(256 << 10)
	for {
		n, e := reader.Read(buf)
		body = append(body, buf[:n]...)
		if int64(len(body)) >= next {
			row.Progress = append(row.Progress, cfTriadProgress{int64(len(body)), cfexpMS(time.Since(row.Started))})
			next += 256 << 10
		}
		if e != nil {
			if e != io.EOF {
				row.Error = "body_read"
				row.ErrorDetail = e.Error()
			}
			break
		}
	}
	row.BodyMS = cfexpMS(time.Since(bodyStart))
	row.Bytes = int64(len(body))
	row.Hash = fmt.Sprintf("%x", sha256.Sum256(body))
	if row.Error == "" && row.Bytes != length {
		row.Error = "incomplete"
	}
	if row.Error == "" && hash != "" && row.Hash != hash {
		row.Error = "content_mismatch"
	}
	return
}

func cfTriadCritical(row cfTriadRow) bool {
	return row.Error == "stop_status" || row.Error == "content_mismatch" || row.Error == "identity"
}

func TestCFTriadExperiment(t *testing.T) {
	if *cfexpOut == "" || *cfTriadCandidates == "" {
		t.Skip("explicit output, frozen candidates and network required")
	}
	input, err := os.ReadFile(*cfTriadCandidates)
	if err != nil {
		t.Fatal(err)
	}
	var pools cfTriadPools
	if err = json.Unmarshal(input, &pools); err != nil {
		t.Fatal(err)
	}
	if _, err = cfTriadPlan(pools, nil, *cfTriadWindow); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal("output must be new:", err)
	}
	w := cfTriadResult{Schema: 1, Method: "cf-triad-v1", CandidatesSHA256: fmt.Sprintf("%x", sha256.Sum256(input)), Window: *cfTriadWindow, PilotOnly: *cfTriadPilotOnly, Started: time.Now(), Outcome: "running"}
	save := func() {
		if err := cfexpSave(filepath.Join(dir, "triad-private.json"), w); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Minute)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	dns := upstreamnet.NewResolver(network)
	defer dns.Close()
	ordinary := cfTriadTransport(dns.DialContext, "")
	defer ordinary.CloseIdleConnections()
	client := &http.Client{Transport: ordinary, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	prep, prepCancel := context.WithTimeout(ctx, 90*time.Second)
	defer prepCancel()
	targets := map[int]cfexpTarget{}
	songs := []int{1343}
	if w.Window == 2 {
		songs = append(songs, 1344)
	}
	for _, song := range songs {
		target, e := cfexpPrepare(prep, client, song)
		if e != nil {
			fail(fmt.Sprintf("prepare_%d_%v", song, e))
		}
		targets[song] = target
		w.BodyBytes++
	}
	if err := cfexpSave(filepath.Join(dir, "targets-private.json"), targets); err != nil {
		fail("save_targets")
	}
	w.DNSIPs, err = dns.ExperimentLookup(prep, cfexpHost)
	if err != nil || len(w.DNSIPs) == 0 {
		fail("dns_discovery")
	}
	slices.Sort(w.DNSIPs)
	w.Candidates, err = cfTriadPlan(pools, w.DNSIPs, w.Window)
	if err != nil {
		fail("candidate_plan")
	}
	for _, ip := range append(append(append([]string(nil), pools.Pilot...), w.DNSIPs...), "") {
		w.Pilot = append(w.Pilot, cfTriadCandidate{ip, "pilot"})
	}
	save()
	refs := map[int][]byte{}
	run := func(stage string, c cfTriadCandidate, song, round, pos int, method string, length int64, timeout time.Duration) (cfTriadRow, []byte) {
		if ctx.Err() != nil {
			fail("window_deadline_or_network")
		}
		if w.BodyBytes+length+1 > 3<<30 {
			fail("body_budget")
		}
		var dial = network.DialContext
		if c.IP == "" {
			dial = dns.DialContext
		}
		tr := cfTriadTransport(dial, c.IP)
		defer tr.CloseIdleConnections()
		hash := ""
		if method != "speed" && refs[song] != nil {
			hash = fmt.Sprintf("%x", sha256.Sum256(refs[song][:length]))
		}
		r, b := cfTriadRead(ctx, tr, targets[song], method, c.IP, length, hash, timeout)
		r.Stage, r.Pool, r.Round, r.Position = stage, c.Pool, round, pos
		w.Rows = append(w.Rows, r)
		w.BodyBytes += r.Bytes
		save()
		if cfTriadCritical(r) {
			fail("critical_" + r.Error)
		}
		if network.err() != nil {
			fail("network_binding")
		}
		return r, b
	}
	for _, song := range songs {
		r, b := run("reference", cfTriadCandidate{"", "reference"}, song, 0, 0, "https", 8<<20, 20*time.Second)
		if r.Error != "" {
			fail("reference_" + r.Error)
		}
		refs[song] = b
	}
	for round := 0; round < 2; round++ {
		for i := 0; i < len(w.Pilot); i++ {
			pos := (i + round*7) % len(w.Pilot)
			c := w.Pilot[pos]
			for _, method := range cfTriadPilotOrder(pos, round, w.Window) {
				length, timeout := int64(8<<20), 20*time.Second
				if method == "speed" {
					length, timeout = 2<<20, 10*time.Second
				}
				run("pilot", c, 1343, round+1, i, method, length, timeout)
			}
			t.Logf("pilot round %d: %d/%d", round+1, i+1, len(w.Pilot))
		}
	}
	if !w.PilotOnly {
		song := 1343
		if w.Window == 2 {
			song = 1344
		}
		audited := map[string]int{}
		for i, c := range w.Candidates {
			order := cfTriadOrder(i + 2*(w.Window-1))
			eligible := false
			for _, method := range order {
				r, _ := run("screen", c, song, 1, i, method, 1024, 4*time.Second)
				eligible = eligible || r.Error == ""
			}
			if !eligible && audited[c.Pool] < 8 {
				audited[c.Pool]++
				eligible = true
			}
			for _, method := range order {
				if eligible {
					run("measure", c, song, 1, i, method, 2<<20, 10*time.Second)
				} else {
					w.Rows = append(w.Rows, cfTriadRow{Stage: "measure", Pool: c.Pool, IP: c.IP, Song: song, Round: 1, Position: i, Method: method, Requested: 2 << 20, Error: "screened_out"})
					save()
				}
			}
			t.Logf("full pool: %d/%d, body %.1f MiB", i+1, len(w.Candidates), float64(w.BodyBytes)/(1<<20))
		}
	}
	w.Outcome = "complete"
}
