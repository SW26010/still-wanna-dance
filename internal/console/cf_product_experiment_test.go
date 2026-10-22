//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

var cfProductHistory = flag.String("cfproduct-history", "", "frozen calibration-01 selection.json; required for the product experiment")

const cfProductHistoryHash = "fb8a2221d52ae2e0f5318d15075dd94663d12da097e24f5303983fad8d3cb640"

type cfProductRequest struct {
	Stage, Host, Pinned, Remote, Range, ContentRange, Cache, Colo, Hash string
	Error, ErrorDetail                                                  string
	StartMS, HeadersMS, EndMS                                           float64
	Status                                                              int
	Length, Bytes                                                       int64
	TLSVerified, Reused, PeerVerified                                   bool
	IgnoredTraceConnections                                             int
}

// Observation only: the product remains responsible for protocol validation,
// retries, complete-file checksums and publication. Monitor intentionally stops
// some bodies early, so partial measurement bodies are not called task failures.
type cfProductRecorder struct {
	mu      sync.Mutex
	stage   string
	started time.Time
	rows    []cfProductRequest
	total   *atomic.Int64
	stop    context.CancelFunc
}

func (c *cfProductRecorder) setStage(stage string) { c.mu.Lock(); c.stage = stage; c.mu.Unlock() }
func (c *cfProductRecorder) snapshot() []cfProductRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.rows)
}
func (c *cfProductRecorder) add(r cfProductRequest) {
	c.mu.Lock()
	c.rows = append(c.rows, r)
	c.mu.Unlock()
}
func (c *cfProductRecorder) wrap(tr http.RoundTripper, ip string) http.RoundTripper {
	return &cfProductTransport{c: c, tr: tr, ip: ip}
}

type cfProductTransport struct {
	c  *cfProductRecorder
	tr http.RoundTripper
	ip string
}

func (t *cfProductTransport) ChannelID() string {
	if c, ok := t.tr.(interface{ ChannelID() string }); ok {
		return c.ChannelID()
	}
	return ""
}
func (t *cfProductTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := t.c
	c.mu.Lock()
	stage := c.stage
	c.mu.Unlock()
	r := cfProductRequest{Stage: stage, Host: req.URL.Hostname(), Pinned: t.ip, Range: req.Header.Get("Range"), StartMS: cfexpMS(time.Since(c.started))}
	var traceMu sync.Mutex
	var remote string
	var reused, peerVerified bool
	var ignored int
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		// DNS requests inherit ClientTrace through the request context. A late
		// resolver GotConn can occur even after the media connection succeeded.
		// Accept only a verified TLS connection for this exact request hostname.
		conn, ok := info.Conn.(interface{ ConnectionState() tls.ConnectionState })
		traceMu.Lock()
		defer traceMu.Unlock()
		if !ok {
			ignored++
			return
		}
		state := conn.ConnectionState()
		if !strings.EqualFold(state.ServerName, req.URL.Hostname()) || len(state.VerifiedChains) == 0 {
			ignored++
			return
		}
		remote, _, _ = net.SplitHostPort(info.Conn.RemoteAddr().String())
		reused, peerVerified = info.Reused, true
	}}
	resp, err := t.tr.RoundTrip(req.Clone(httptrace.WithClientTrace(req.Context(), trace)))
	traceMu.Lock()
	r.Remote, r.Reused, r.PeerVerified, r.IgnoredTraceConnections = remote, reused, peerVerified, ignored
	traceMu.Unlock()
	r.HeadersMS = cfexpMS(time.Since(c.started))
	if err != nil {
		r.Remote = "" // Nested DoH GotConn is not proof of a media connection.
		r.PeerVerified = false
		r.Error, r.ErrorDetail, r.EndMS = "transport", err.Error(), r.HeadersMS
		c.add(r)
		return nil, err
	}
	r.Status, r.Length, r.ContentRange, r.Cache = resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Range"), resp.Header.Get("CF-Cache-Status")
	r.TLSVerified = resp.TLS != nil && len(resp.TLS.VerifiedChains) > 0
	if ray := resp.Header.Get("CF-Ray"); strings.Contains(ray, "-") {
		r.Colo = ray[strings.LastIndexByte(ray, '-')+1:]
	}
	if !r.TLSVerified || !r.PeerVerified || (t.ip != "" && r.Remote != t.ip) {
		r.Error = "tls_or_peer"
		c.stop()
	}
	if r.Status == 429 || r.Status == 503 {
		r.Error = "stop_status"
		c.stop()
	}
	resp.Body = &cfProductBody{ReadCloser: resp.Body, c: c, row: r, h: sha256.New()}
	return resp, nil
}

type cfProductBody struct {
	io.ReadCloser
	mu   sync.Mutex
	once sync.Once
	c    *cfProductRecorder
	row  cfProductRequest
	h    hash.Hash
}

func (b *cfProductBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.ReadCloser.Read(p)
	b.row.Bytes += int64(n)
	b.h.Write(p[:n])
	if b.c.total.Add(int64(n)) > cfSelectionBudget {
		b.row.Error = "byte_budget"
		b.c.stop()
	}
	if err != nil && err != io.EOF {
		b.row.Error, b.row.ErrorDetail = "body_read", err.Error()
	}
	return n, err
}
func (b *cfProductBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.row.EndMS, b.row.Hash = cfexpMS(time.Since(b.c.started)), fmt.Sprintf("%x", b.h.Sum(nil))
		b.c.add(b.row)
	})
	return err
}

type cfProductProvider struct {
	*cfexpProvider
	recorder *cfProductRecorder
}

func (p *cfProductProvider) Candidates(ctx context.Context, target string) ([]upstreamrequest.Candidate, error) {
	cs, err := p.cfexpProvider.Candidates(ctx, target)
	return p.wrap(cs), err
}
func (p *cfProductProvider) Current(target string) []upstreamrequest.Candidate {
	return p.wrap(p.cfexpProvider.Current(target))
}
func (p *cfProductProvider) wrap(cs []upstreamrequest.Candidate) []upstreamrequest.Candidate {
	for i := range cs {
		cs[i].Transport = p.recorder.wrap(cs[i].Transport, cs[i].IP)
	}
	return cs
}

type cfProductFile struct {
	MD5   string
	Size  int64
	Valid bool
}
type cfProductTask struct {
	Song                        int
	Source, Error, ErrorDetail  string
	Started                     time.Time
	ElapsedMS, SessionElapsedMS float64
	OK                          bool
	Files                       []cfProductFile
	Selections                  []string
}
type cfProductSession struct {
	Round                            int
	Arm, Error, SetupError, Selected string
	Started, Finished                time.Time
	SetupMS                          float64
	SetupBytes                       int64
	Probes                           []cfSelectionProbe
	Tasks                            []cfProductTask
	Requests                         []cfProductRequest
}
type cfProductWindow struct {
	Schema                                int
	Method, Outcome, Error, HistorySHA256 string
	Started, Finished                     time.Time
	BodyBytes                             int64
	Sessions                              []*cfProductSession
}

func cfProductFiles(root string) ([]cfProductFile, error) {
	paths, err := filepath.Glob(filepath.Join(root, "videos", "*.mp4"))
	if err != nil {
		return nil, err
	}
	var out []cfProductFile
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return out, err
		}
		h := md5.New()
		n, copyErr := io.Copy(h, f)
		f.Close()
		if copyErr != nil {
			return out, copyErr
		}
		digest := fmt.Sprintf("%x", h.Sum(nil))
		out = append(out, cfProductFile{MD5: digest, Size: n, Valid: strings.EqualFold(strings.TrimSuffix(filepath.Base(path), ".mp4"), digest)})
	}
	return out, nil
}

func cfProductRun(ctx context.Context, network *cfNetwork, root string, s *cfProductSession, reference map[int64]string, total *atomic.Int64, stop context.CancelFunc, save func()) {
	s.Started = time.Now()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	recorder := &cfProductRecorder{started: s.Started, stage: "setup", total: total, stop: stop}
	defer func() { s.Finished = time.Now(); s.Requests = recorder.snapshot(); save() }()
	dns := &directDNS{dialer: network}
	defer dns.close()
	ordinary := &http.Transport{DialContext: dns.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer ordinary.CloseIdleConnections()
	base := recorder.wrap(ordinary, "")
	client := &http.Client{Transport: base, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	pool, err := network.pool(dns)
	if err != nil {
		s.Error = "pool_init"
		return
	}
	defer pool.Close()
	channel := upstreamrequest.NewChannel()
	provider := &cfProductProvider{cfexpProvider: &cfexpProvider{api: pool, resource: pool, ordinary: base}, recorder: recorder}
	channel.Publish(provider)
	// The experiment is a separate process and all sessions are serial. Use the
	// actual business resource-registration channel without touching app state.
	previous := upstreamrequest.Default
	upstreamrequest.Default = channel
	defer func() { upstreamrequest.Default = previous }()
	c := &Console{apiBase: "https://api.udon.dance", client: client, settings: Settings{UpstreamMode: "direct", DownloadUpstream: "cf"}}
	var historyTransport http.RoundTripper
	setupStart, setupBytes := time.Now(), total.Load()
	setupCtx, setupCancel := context.WithTimeout(ctx, 3*time.Minute)
	if s.Arm == "monitor" {
		m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel, PlaybackSongCandidates: func(context.Context) []int64 { return []int64{1343} }})
		if err != nil {
			setupCancel()
			s.Error = "monitor_init"
			return
		}
		defer m.Close()
		c.monitor = m
		if err := m.CheckSelected(setupCtx, upstreamstate.CheckThroughput); err != nil {
			s.SetupError = "monitor_check"
		}
		if err := cfexpSave(filepath.Join(root, "monitor-private.json"), m.Snapshot()); err != nil {
			setupCancel()
			s.Error = "save_monitor"
			return
		}
	} else if s.Arm == "history-two" {
		target, err := cfexpPrepare(setupCtx, client, 1343)
		if err != nil {
			s.SetupError = err.Error()
		} else {
			ips := []string{"2606:4700:20::681a:41c", "104.16.0.1"}
			if s.Round%2 == 0 {
				slices.Reverse(ips)
			}
			best := float64(0)
			for _, ip := range ips {
				if setupCtx.Err() != nil {
					break
				}
				p := cfSelectionShort(setupCtx, network, target, ip, 8<<20, reference, total, stop)
				s.Probes = append(s.Probes, p)
				if p.Error == "" && (s.Selected == "" || p.ElapsedMS < best) {
					s.Selected, best = ip, p.ElapsedMS
				}
			}
			if s.Selected != "" {
				tr, closePool, err := cfFamilyTransport(ctx, network, cfexpStatic{[]string{s.Selected}, time.Now().Add(5 * time.Minute)}, target.URL, s.Selected)
				if err != nil {
					s.SetupError = "history_pool"
				} else {
					defer closePool()
					historyTransport = recorder.wrap(tr, s.Selected)
				}
			}
		}
	}
	setupCancel()
	s.SetupMS, s.SetupBytes = cfexpMS(time.Since(setupStart)), total.Load()-setupBytes
	s.Requests = recorder.snapshot()
	save()
	if ctx.Err() != nil {
		s.Error = "setup_context"
		return
	}
	if s.SetupError != "" {
		s.Error = "setup_failed"
		return
	}
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = filepath.Join(root, "storage")
	cfg.CacheUnlimited, cfg.DialContext = true, dns.DialContext
	cfg.BeginResourceLoad = channel.BeginResourceLoad
	cfg.Logger = slog.Default()
	cfg.ResourceTransports = func(target string) []http.RoundTripper {
		if s.Arm == "history-two" && historyTransport != nil {
			return []http.RoundTripper{historyTransport, base}
		}
		var trs []http.RoundTripper
		for _, selected := range c.operationSelections(upstreamstate.Resource, upstreamstate.Constraints{Target: target}) {
			trs = append(trs, selected.Channel.Transport)
		}
		if len(trs) == 0 {
			return []http.RoundTripper{base}
		}
		return trs
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		s.Error = "engine_init"
		return
	}
	defer engine.Close()
	c.service = engine
	songs := slices.Clone(cfexpSongs)
	songs = append(songs[(s.Round-1)%3:], songs[:(s.Round-1)%3]...)
	for _, song := range songs {
		if ctx.Err() != nil {
			s.Error = "session_context"
			return
		}
		recorder.setStage(fmt.Sprintf("task-%d", song))
		previousFiles := 0
		if len(s.Tasks) > 0 {
			previousFiles = len(s.Tasks[len(s.Tasks)-1].Files)
		}
		r := cfProductTask{Song: song, Started: time.Now()}
		for _, selected := range c.operationSelections(upstreamstate.Resource, upstreamstate.Constraints{Target: "https://" + cfexpHost}) {
			r.Selections = append(r.Selections, selected.Channel.IP)
		}
		if historyTransport != nil {
			r.Selections = []string{s.Selected}
		}
		attempt, done := context.WithTimeout(ctx, 90*time.Second)
		r.Source, err = c.prefetchSong(attempt, engine, int64(song))
		r.ElapsedMS, r.SessionElapsedMS = cfexpMS(time.Since(r.Started)), cfexpMS(time.Since(s.Started))
		done()
		if err != nil {
			r.Error, r.ErrorDetail = "prefetch_failed", err.Error()
		}
		r.Files, err = cfProductFiles(cfg.StorageDir)
		if err != nil {
			r.Error, r.ErrorDetail = "file_check", err.Error()
		}
		if r.Error == "" && (r.Source != "MISS" || len(r.Files) != previousFiles+1 || slices.ContainsFunc(r.Files, func(f cfProductFile) bool { return !f.Valid })) {
			r.Error = "publication"
			stop()
		}
		var retained int64
		for _, f := range r.Files {
			retained += f.Size
		}
		if retained > 200<<20 {
			r.Error = "resource_size_budget"
			stop()
		}
		r.OK = r.Error == ""
		s.Tasks = append(s.Tasks, r)
		s.Requests = recorder.snapshot()
		save()
	}
}

func TestCFProductExperiment(t *testing.T) { cfRunProductExperiment(t, false) }
func TestCFProductTraceSmoke(t *testing.T) { cfRunProductExperiment(t, true) }

func cfRunProductExperiment(t *testing.T, smoke bool) {
	if *cfexpOut == "" {
		t.Skip("explicit output and Wi-Fi configuration required")
	}
	raw, err := os.ReadFile(*cfProductHistory)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != cfProductHistoryHash {
		t.Fatal("frozen history missing or changed")
	}
	var history cfSelectionWindow
	if err := json.Unmarshal(raw, &history); err != nil || len(history.Training) == 0 || !history.Training[0].OK {
		t.Fatal("invalid history reference")
	}
	dir, err := filepath.Abs(*cfexpOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal("new output required:", err)
	}
	w := cfProductWindow{Schema: 1, Method: "actual-prefetch-cf-history-two-v2", Outcome: "running", HistorySHA256: cfProductHistoryHash, Started: time.Now()}
	var total atomic.Int64
	save := func() {
		w.BodyBytes = total.Load()
		if err := cfexpSave(filepath.Join(dir, "product.json"), w); err != nil {
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
	logFile, err := os.Create(filepath.Join(dir, "engine-private.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logFile, nil)))
	defer slog.SetDefault(previousLog)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	network := cfexpStartNetwork(t, ctx, cancel, dir)
	arms := []string{"ordinary", "monitor", "history-two"}
	for round := 1; round <= 3; round++ {
		for j := range arms {
			if ctx.Err() != nil {
				w.Error = "window_context"
				t.Fatal(w.Error)
			}
			arm := arms[(j+round-1)%len(arms)]
			s := &cfProductSession{Round: round, Arm: arm}
			w.Sessions = append(w.Sessions, s)
			root := filepath.Join(dir, fmt.Sprintf("%d-%s", round, arm))
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			t.Logf("session round=%d arm=%s", round, arm)
			cfProductRun(ctx, network, root, s, cfSelectionHashes(history.Training[0]), &total, cancel, save)
			for _, r := range s.Tasks {
				t.Logf("task round=%d arm=%s song=%d ok=%t elapsed=%.1fms session=%.1fms", round, arm, r.Song, r.OK, r.ElapsedMS, r.SessionElapsedMS)
			}
			t.Logf("session result setup=%.1fms bytes=%d selected=%s error=%s", s.SetupMS, s.SetupBytes, s.Selected, s.Error)
			if smoke {
				if ctx.Err() != nil || network.err() != nil || s.Error != "" || len(s.Tasks) != 3 || slices.ContainsFunc(s.Tasks, func(r cfProductTask) bool { return !r.OK }) {
					w.Error = "diagnostic_failed"
					t.Fatal(w.Error)
				}
				w.Outcome = "diagnostic-completed"
				return
			}
		}
	}
	if err := network.err(); err != nil {
		w.Error = "network_binding"
		t.Fatal(w.Error)
	}
	w.Outcome = "completed"
}

// A failed outer HTTP request must not inherit a nested resolver's connection.
func TestCFProductRecorderFailure(t *testing.T) {
	var total atomic.Int64
	c := &cfProductRecorder{started: time.Now(), total: &total, stop: func() {}}
	tr := c.wrap(cfexpFunc(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{Conn: cfexpConn{}})
		return nil, errors.New("nested DNS failure")
	}), "")
	req, _ := http.NewRequest("GET", "https://"+cfexpHost, nil)
	_, err := tr.RoundTrip(req)
	rows := c.snapshot()
	if err == nil || len(rows) != 1 || rows[0].Remote != "" || rows[0].Error != "transport" {
		t.Fatal(rows, err)
	}
}

func TestCFProductRecorderCountsPartialBodiesOnce(t *testing.T) {
	var total atomic.Int64
	c := &cfProductRecorder{started: time.Now(), stage: "setup", total: &total, stop: func() { t.Error("verified measurement must not cancel") }}
	tr := c.wrap(cfexpFunc(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{Conn: cfProductTLSConn{host: r.URL.Hostname(), peer: "104.16.0.1"}})
		return &http.Response{StatusCode: 206, ContentLength: 8, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("12345678")),
			TLS: &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}}, nil
	}), "")
	req, _ := http.NewRequest("GET", "https://"+cfexpHost, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 3)); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp.Body.Close()
	rows := c.snapshot()
	if total.Load() != 3 || len(rows) != 1 || rows[0].Bytes != 3 || rows[0].Length != 8 || rows[0].Error != "" {
		t.Fatal(total.Load(), rows)
	}
}

func TestCFProductPrefetchIntegration(t *testing.T) {
	// Exercise the actual Console API resolution and publication path, including
	// a failed resource transport that must remain visible after fallback succeeds.
	previous := upstreamrequest.Default
	upstreamrequest.Default = upstreamrequest.NewChannel()
	defer func() { upstreamrequest.Default = previous }()
	payload := strings.Repeat("x", (1<<20)+31)
	target := fmt.Sprintf("https://%s/files/2403/1344-fixture.mp4?e=%x&s=%d", cfexpHost, md5.Sum([]byte(payload)), len(payload))
	var total atomic.Int64
	recorder := &cfProductRecorder{started: time.Now(), stage: "task-1344", total: &total, stop: func() { t.Error("unexpected integrity cancellation") }}
	tr := recorder.wrap(cfexpFunc(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{Conn: cfProductTLSConn{host: r.URL.Hostname(), peer: "104.16.0.1"}})
		h := make(http.Header)
		resp := &http.Response{Header: h, Request: r, TLS: &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}}
		if r.URL.Hostname() == "api.udon.dance" {
			resp.StatusCode, resp.Body = 302, http.NoBody
			h.Set("Location", target)
			return resp, nil
		}
		var first, last int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			return nil, err
		}
		body := payload[first : last+1]
		resp.StatusCode, resp.ContentLength, resp.Body = 206, int64(len(body)), io.NopCloser(strings.NewReader(body))
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(payload)))
		h.Set("ETag", `"fixture"`)
		return resp, nil
	}), "")
	broken := recorder.wrap(cfexpFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("planned fallback fixture") }), "")
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir, cfg.CacheUnlimited = t.TempDir(), true
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.ResourceTransports = func(string) []http.RoundTripper { return []http.RoundTripper{broken, tr} }
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	c := &Console{apiBase: "https://api.udon.dance", service: engine, settings: Settings{UpstreamMode: "direct", DownloadUpstream: "cf"},
		client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := c.prefetchSong(ctx, engine, 1344)
	if err != nil || source != "MISS" {
		t.Fatal(source, err)
	}
	files, err := cfProductFiles(cfg.StorageDir)
	if err != nil || len(files) != 1 || !files[0].Valid || files[0].Size != int64(len(payload)) {
		t.Fatal(files, err)
	}
	rows := recorder.snapshot()
	if total.Load() != int64(len(payload)) || !slices.ContainsFunc(rows, func(r cfProductRequest) bool { return r.Error == "transport" }) {
		t.Fatal(total.Load(), rows)
	}
}

type cfProductTLSConn struct {
	cfexpConn
	host, peer string
}

func (c cfProductTLSConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{ServerName: c.host, VerifiedChains: [][]*x509.Certificate{{{}}}}
}
func (c cfProductTLSConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(c.peer), Port: 443}
}

func TestCFProductRecorderIgnoresLateDNSOnSuccess(t *testing.T) {
	var total atomic.Int64
	c := &cfProductRecorder{started: time.Now(), total: &total, stop: func() { t.Error("target peer lost") }}
	var trace *httptrace.ClientTrace
	tr := c.wrap(cfexpFunc(func(r *http.Request) (*http.Response, error) {
		trace = httptrace.ContextClientTrace(r.Context())
		trace.GotConn(httptrace.GotConnInfo{Conn: cfProductTLSConn{host: cfexpHost, peer: "104.16.0.1"}})
		trace.GotConn(httptrace.GotConnInfo{Conn: cfProductTLSConn{host: "doh.pub", peer: "1.12.12.12"}})
		return &http.Response{StatusCode: 206, ContentLength: 0, Header: make(http.Header), Body: http.NoBody,
			TLS: &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}}, nil
	}), "")
	req, _ := http.NewRequest("GET", "https://"+cfexpHost, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		trace.GotConn(httptrace.GotConnInfo{Conn: cfProductTLSConn{host: "doh.pub", peer: "1.12.12.12"}})
		close(done)
	}()
	resp.Body.Close()
	<-done
	rows := c.snapshot()
	if len(rows) != 1 || rows[0].Remote != "104.16.0.1" || !rows[0].PeerVerified || rows[0].IgnoredTraceConnections != 1 {
		t.Fatal(rows)
	}
}
