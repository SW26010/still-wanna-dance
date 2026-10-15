//go:build windows

package cacheproxy

// Opt-in development experiment. The origin runs in a separate process; normal
// tests skip this entry point. Candidate worker counts use a Go build overlay,
// never a runtime product setting or an edit to the product default.
import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"still-wanna-dance/internal/blocksched"
)

type followPlan struct {
	Mode, ID, OriginInfo, Out, Storage, Scenario string
	N, RateMiB, Blocks, Seed                     int
	Warm                                         bool
}
type followOrigin struct {
	Address, Certificate string
	RateMiB              int
}
type followRequest struct {
	StartMS      float64 `json:"startMS"`
	ElapsedMS    float64 `json:"elapsedMS"`
	Bytes        int64   `json:"bytes"`
	Status, Seed int
	Range        string
	Failed       bool
}

type followSample struct {
	AtMS                                     float64 `json:"atMS"`
	UserMS                                   float64 `json:"userMS"`
	KernelMS                                 float64 `json:"kernelMS"`
	HeapBytes, PrivateBytes, WorkingSetBytes uint64
	Handles                                  uint32
	Goroutines                               int `json:"goroutines"`
}
type followReport struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Target      int      `json:"target"`
	PID         int      `json:"pid"`
	LogicalCPUs int      `json:"logicalCPUs"`
	CoreMasks   []uint64 `json:"coreMasks"`
	Stages      []struct {
		N int `json:"n"`
	} `json:"stages"`
	EndMS                                                       float64         `json:"endMS"`
	StopMS                                                      float64         `json:"stopMS"`
	Samples                                                     []followSample  `json:"samples"`
	Requests                                                    []followRequest `json:"requests"`
	OK                                                          bool            `json:"ok"`
	Error                                                       string          `json:"error,omitempty"`
	GCs                                                         uint32          `json:"gcs"`
	GCPauseMS                                                   float64         `json:"gcPauseMS"`
	AllocBytes                                                  uint64          `json:"allocBytes"`
	Plan                                                        followPlan      `json:"plan"`
	PeakRequests, Connections, ReusedConnections, TLSHandshakes int
	FirstBodyMS, SeekMS, CompleteMS, CancelWaitMS, CloseMS      float64
	PublishedFiles, PartialsAfterClose, ActiveAfterClose        int
	BudgetTotal, BudgetBackground, BackgroundWorkers            int
	OriginStats                                                 map[string]any
}

func followSave(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func followPattern(seed int) []byte {
	b := make([]byte, 1<<20)
	for i := range b {
		b[i] = byte((i*17 + i/251 + seed*71) % 251)
	}
	return b
}
func followURL(seed, blocks int) string {
	h := md5.New()
	b := followPattern(seed)
	for range blocks {
		h.Write(b)
	}
	return fmt.Sprintf("https://play.udon.dance/files/2403/1344-660524b4ebadb.mp4?e=%x&s=%d&seed=%d", h.Sum(nil), blocks*(1<<20), seed)
}

func followServe(p followPlan) error {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(20261014), Subject: pkix.Name{CommonName: "Local CPU experiment"}, DNSNames: []string{"play.udon.dance"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(8 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		return e
	}
	patterns := [][]byte{followPattern(0), followPattern(1), followPattern(2)}
	var mu sync.Mutex
	var next time.Time
	var bytesSent int64
	active, peak, requests := 0, 0, 0
	deadline := time.Now().Add(45 * time.Minute)
	quit := make(chan struct{})
	var once sync.Once
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stats" {
			mu.Lock()
			v := map[string]any{"bytes": bytesSent, "active": active, "peak": peak, "requests": requests}
			mu.Unlock()
			json.NewEncoder(w).Encode(v)
			return
		}
		if r.URL.Path == "/reset" {
			mu.Lock()
			defer mu.Unlock()
			if active != 0 {
				http.Error(w, "busy", 409)
				return
			}
			bytesSent = 0
			peak = 0
			requests = 0
			next = time.Time{}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/quit" {
			once.Do(func() { close(quit) })
			w.WriteHeader(204)
			return
		}
		seed, e := strconv.Atoi(r.URL.Query().Get("seed"))
		size, se := strconv.ParseInt(r.URL.Query().Get("s"), 10, 64)
		var start, end int64
		_, re := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if e != nil || se != nil || re != nil || seed < 0 || seed > 2 || size < 1 || size > 128<<20 || start < 0 || end < start || end >= size {
			http.Error(w, "invalid fixture range", 400)
			return
		}
		mu.Lock()
		if bytesSent >= 16<<30 || time.Now().After(deadline) {
			mu.Unlock()
			http.Error(w, "fixture bound", 503)
			return
		}
		active++
		peak = max(peak, active)
		requests++
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		select {
		case <-time.After(20 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"fixture-%d-%d"`, seed, size))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		for pos := start; pos <= end; {
			count := min(int64(64<<10), end-pos+1, int64(len(patterns[seed]))-pos%(1<<20))
			mu.Lock()
			if next.Before(time.Now()) {
				next = time.Now()
			}
			next = next.Add(time.Duration(float64(count) / float64(p.RateMiB*(1<<20)) * float64(time.Second)))
			due := next
			mu.Unlock()
			timer := time.NewTimer(time.Until(due))
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			n, err := w.Write(patterns[seed][pos%(1<<20) : pos%(1<<20)+count])
			mu.Lock()
			bytesSent += int64(n)
			mu.Unlock()
			if err != nil {
				return
			}
			pos += int64(n)
			w.(http.Flusher).Flush()
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	srv.EnableHTTP2 = false
	srv.Config.ErrorLog = nil
	srv.StartTLS()
	defer srv.Close()
	info := followOrigin{strings.TrimPrefix(srv.URL, "https://"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), p.RateMiB}
	if e = followSave(p.Out, info); e != nil {
		return e
	}
	select {
	case <-quit:
	case <-time.After(time.Until(deadline)):
	}
	return nil
}

type followTransport struct {
	base                                   *http.Transport
	mu                                     sync.Mutex
	zero                                   time.Time
	requests                               []followRequest
	active, peak, connections, reused, tls int
}
type followBody struct {
	io.ReadCloser
	t      *followTransport
	record followRequest
	start  time.Time
	once   sync.Once
	bytes  atomic.Int64
	failed atomic.Bool
}

func (b *followBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	b.bytes.Add(int64(n))
	if e != nil && e != io.EOF {
		b.failed.Store(true)
	}
	return n, e
}
func (b *followBody) Close() error {
	e := b.ReadCloser.Close()
	b.once.Do(func() {
		b.record.Bytes = b.bytes.Load()
		b.record.Failed = b.failed.Load()
		b.record.ElapsedMS = float64(time.Since(b.start)) / 1e6
		b.t.mu.Lock()
		b.t.active--
		b.t.requests = append(b.t.requests, b.record)
		b.t.mu.Unlock()
	})
	return e
}
func (t *followTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	seed, _ := strconv.Atoi(r.URL.Query().Get("seed"))
	record := followRequest{StartMS: float64(start.Sub(t.zero)) / 1e6, Range: r.Header.Get("Range"), Seed: seed}
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		t.mu.Lock()
		if i.Reused {
			t.reused++
		} else {
			t.connections++
		}
		t.mu.Unlock()
	}, TLSHandshakeDone: func(_ tls.ConnectionState, _ error) { t.mu.Lock(); t.tls++; t.mu.Unlock() }}
	t.mu.Lock()
	t.active++
	t.peak = max(t.peak, t.active)
	t.mu.Unlock()
	resp, e := t.base.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
	if e != nil {
		record.Failed = true
		record.ElapsedMS = float64(time.Since(start)) / 1e6
		t.mu.Lock()
		t.active--
		t.requests = append(t.requests, record)
		t.mu.Unlock()
		return nil, e
	}
	record.Status = resp.StatusCode
	resp.Body = &followBody{ReadCloser: resp.Body, t: t, record: record, start: start}
	return resp, nil
}
func followClient(info followOrigin) (*http.Client, *http.Transport, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(info.Certificate)) {
		return nil, nil, errors.New("bad fixture certificate")
	}
	host, _, e := net.SplitHostPort(info.Address)
	if e != nil || host != "127.0.0.1" {
		return nil, nil, errors.New("fixture must be IPv4 loopback")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	tr := newTransport()
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return d.DialContext(ctx, network, info.Address)
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "play.udon.dance", MinVersion: tls.VersionTLS12}
	tr.MaxIdleConns = 64
	tr.MaxIdleConnsPerHost = 64
	tr.ForceAttemptHTTP2 = false
	return &http.Client{Transport: tr, Timeout: 45 * time.Second}, tr, nil
}
func followControl(c *http.Client, path string, out any) error {
	r, e := c.Get("https://play.udon.dance" + path)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		return fmt.Errorf("fixture control %d", r.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(r.Body).Decode(out)
	}
	io.Copy(io.Discard, r.Body)
	return nil
}
func followRaw(ctx context.Context, c *http.Client, n, blocks int, url string, want [32]byte) error {
	var next atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			buf := make([]byte, (1<<20)+1)
			for {
				block := next.Add(1) - 1
				if block >= int64(blocks) {
					return
				}
				start := block % (96) * (1 << 20)
				r, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
				r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+(1<<20)-1))
				r.Header.Set("Accept-Encoding", "identity")
				resp, e := c.Do(r)
				if e != nil {
					errs <- e
					return
				}
				if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, start+(1<<20)-1, 96<<20) || resp.Header.Get("ETag") != `"fixture-0-100663296"` {
					resp.Body.Close()
					errs <- errors.New("raw range headers")
					return
				}
				data, e := readRangeBlock(resp.Body, buf)
				resp.Body.Close()
				if e != nil || len(data) != 1<<20 || sha256.Sum256(data) != want {
					errs <- errors.New("raw body verification")
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		return e
	}
	return nil
}

var followKernel = windows.NewLazySystemDLL("kernel32.dll")
var followPSAPI = windows.NewLazySystemDLL("psapi.dll")

type followMemory struct {
	CB, Faults                                                                                            uint32
	PeakWorkingSet, WorkingSet, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile, Private uintptr
}

func followSampleNow(zero time.Time) followSample {
	var creation, exit, kernel, user windows.Filetime
	windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user)
	cv := func(f windows.Filetime) float64 {
		return float64(uint64(f.HighDateTime)<<32|uint64(f.LowDateTime)) / 10000
	}
	mem := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(mem)
	m := followMemory{}
	m.CB = uint32(unsafe.Sizeof(m))
	followPSAPI.NewProc("GetProcessMemoryInfo").Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&m)), uintptr(m.CB))
	var handles uint32
	followKernel.NewProc("GetProcessHandleCount").Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&handles)))
	return followSample{AtMS: float64(time.Since(zero)) / 1e6, UserMS: cv(user), KernelMS: cv(kernel), HeapBytes: mem[0].Value.Uint64(), PrivateBytes: uint64(m.Private), WorkingSetBytes: uint64(m.WorkingSet), Handles: handles, Goroutines: runtime.NumGoroutine()}
}
func followMasks() ([]uint64, error) {
	proc := followKernel.NewProc("GetLogicalProcessorInformationEx")
	var size uint32
	proc.Call(0, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return nil, errors.New("topology unavailable")
	}
	b := make([]byte, size)
	ok, _, e := proc.Call(0, uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return nil, e
	}
	var masks []uint64
	for p := 0; p < len(b); {
		if p+48 > len(b) {
			return nil, errors.New("topology record")
		}
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		if n < 48 || p+n > len(b) || binary.LittleEndian.Uint16(b[p+30:]) != 1 || binary.LittleEndian.Uint16(b[p+40:]) != 0 {
			return nil, errors.New("topology groups")
		}
		masks = append(masks, binary.LittleEndian.Uint64(b[p+32:]))
		p += n
	}
	return masks, nil
}
func followMarker() (func(string) error, func(), error) {
	dll := windows.NewLazySystemDLL("advapi32.dll")
	guid, _ := windows.GUIDFromString("{E3D074A8-25FB-4D47-9427-3BC102CF5D31}")
	var handle uint64
	code, _, _ := dll.NewProc("EventRegister").Call(uintptr(unsafe.Pointer(&guid)), 0, 0, uintptr(unsafe.Pointer(&handle)))
	if code != 0 {
		return nil, nil, fmt.Errorf("register %d", code)
	}
	return func(s string) error {
		p, e := windows.UTF16PtrFromString(s)
		if e != nil {
			return e
		}
		code, _, _ := dll.NewProc("EventWriteString").Call(uintptr(handle), 4, 1, uintptr(unsafe.Pointer(p)))
		if code != 0 {
			return fmt.Errorf("marker %d", code)
		}
		return nil
	}, func() { dll.NewProc("EventUnregister").Call(uintptr(handle)) }, nil
}

func followEngine(ctx context.Context, p followPlan, tr *followTransport, r *followReport, urls []string, want []byte) (retErr error) {
	cfg := fixtureConfig()
	cfg.StorageDir = p.Storage
	cfg.DownloadTimeout = 40 * time.Second
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.ResourceTransports = func(string) []http.RoundTripper { return []http.RoundTripper{tr} }
	s, e := New(cfg)
	if e != nil {
		return e
	}
	defer func() {
		start := time.Now()
		closeErr := s.Close()
		r.CloseMS = float64(time.Since(start)) / 1e6
		if retErr == nil {
			retErr = closeErr
		}
		filepath.WalkDir(p.Storage, func(path string, d os.DirEntry, e error) error {
			if e == nil && !d.IsDir() && strings.HasSuffix(path, ".part") {
				r.PartialsAfterClose++
			}
			return nil
		})
	}()
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: p.N, GrowInterval: 250 * time.Millisecond, Adaptive: true})
	// These legacy report columns now describe ordinary admission's soft cap;
	// Blocking bypasses it and there is no separate background quota.
	r.BudgetTotal, r.BudgetBackground = p.N, p.N
	r.BackgroundWorkers = 0 // global block scheduling; no per-video worker count
	if p.Scenario == "prefetch" {
		start := time.Now()
		_, e = s.Prefetch(ctx, urls[0])
		r.CompleteMS = float64(time.Since(start)) / 1e6
		if e == nil {
			r.PublishedFiles = 1
		}
		return e
	}
	bgCtx, cancelBG := context.WithCancel(ctx)
	defer cancelBG()
	bgDone := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) { _, e := s.Prefetch(bgCtx, urls[i]); bgDone <- e }(i)
	}
	time.Sleep(250 * time.Millisecond)
	if p.Scenario == "cancel" {
		start := time.Now()
		cancelBG()
		for range 2 {
			e := <-bgDone
			if !errors.Is(e, context.Canceled) {
				return fmt.Errorf("cancel waiter: %v", e)
			}
		}
		r.CancelWaitMS = float64(time.Since(start)) / 1e6
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", urls[2], nil)
	v, e := s.parseResolved(req)
	if e != nil {
		return e
	}
	_, release := s.addDemand(ctx, v, true)
	defer release()
	start := time.Now()
	f, reader, e := s.obtain(ctx, v)
	if e != nil {
		return e
	}
	if reader == nil {
		return errors.New("expected cold reader")
	}
	defer reader.Close()
	buf := make([]byte, 64<<10)
	_, e = io.ReadFull(reader, buf)
	r.FirstBodyMS = float64(time.Since(start)) / 1e6
	if e != nil {
		return e
	}
	if string(buf) != string(want[:len(buf)]) {
		return errors.New("first reader bytes")
	}
	start = time.Now()
	offset := int64(p.Blocks-2) * (1 << 20)
	if _, e = reader.Seek(offset, io.SeekStart); e != nil {
		return e
	}
	_, e = io.ReadFull(reader, buf)
	r.SeekMS = float64(time.Since(start)) / 1e6
	if e != nil {
		return e
	}
	if string(buf) != string(want[:len(buf)]) {
		return errors.New("seek reader bytes")
	}
	reader.Close()
	select {
	case <-f.done:
		if f.err != nil {
			return f.err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	r.CompleteMS = float64(time.Since(tr.zero)) / 1e6
	for range 2 {
		if e := <-bgDone; e != nil {
			return e
		}
	}
	r.PublishedFiles = 3
	return nil
}

func followRun(p followPlan) error {
	b, e := os.ReadFile(p.OriginInfo)
	if e != nil {
		return e
	}
	var origin followOrigin
	if e = json.Unmarshal(b, &origin); e != nil {
		return e
	}
	control, controlTr, e := followClient(origin)
	if e != nil {
		return e
	}
	defer controlTr.CloseIdleConnections()
	client, base, e := followClient(origin)
	if e != nil {
		return e
	}
	defer base.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	urls := []string{followURL(0, p.Blocks), followURL(1, p.Blocks), followURL(2, p.Blocks)}
	rawURL := followURL(0, 96)
	rawHash := sha256.Sum256(followPattern(0))
	readerPattern := followPattern(2)
	if p.Warm {
		if e = followRaw(ctx, client, p.N, p.N, rawURL, rawHash); e != nil {
			return e
		}
	}
	if e = followControl(control, "/reset", nil); e != nil {
		return e
	}
	mark, unregister, e := followMarker()
	if e != nil {
		return e
	}
	defer unregister()
	r := followReport{ID: p.ID, Kind: "burst", Target: p.N, PID: os.Getpid(), LogicalCPUs: runtime.NumCPU(), Plan: p, OK: true}
	r.CoreMasks, e = followMasks()
	if e != nil {
		return e
	}
	tr := &followTransport{base: base}
	client.Transport = tr
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	zero := time.Now()
	tr.zero = zero
	if e = mark("CFCPU|" + p.ID + "|begin"); e != nil {
		return e
	}
	r.Stages = append(r.Stages, struct {
		N int `json:"n"`
	}{p.N})
	if e = mark("CFCPU|" + p.ID + "|stage"); e != nil {
		return e
	}
	r.Samples = append(r.Samples, followSampleNow(zero))
	done := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				r.Samples = append(r.Samples, followSampleNow(zero))
			}
		}
	}()
	if p.Mode == "idle" {
		time.Sleep(6 * time.Second)
	} else if p.Mode == "raw" {
		e = followRaw(ctx, client, p.N, p.Blocks, rawURL, rawHash)
	} else {
		e = followEngine(ctx, p, tr, &r, urls, readerPattern)
	}
	r.StopMS = float64(time.Since(zero)) / 1e6
	mark("CFCPU|" + p.ID + "|stop")
	base.CloseIdleConnections()
	r.EndMS = float64(time.Since(zero)) / 1e6
	mark("CFCPU|" + p.ID + "|end")
	close(done)
	<-samplerDone
	r.Samples = append(r.Samples, followSampleNow(zero))
	runtime.ReadMemStats(&after)
	r.AllocBytes = after.TotalAlloc - before.TotalAlloc
	r.GCs = after.NumGC - before.NumGC
	r.GCPauseMS = float64(after.PauseTotalNs-before.PauseTotalNs) / 1e6
	tr.mu.Lock()
	r.Requests = tr.requests
	r.PeakRequests = tr.peak
	r.Connections = tr.connections
	r.ReusedConnections = tr.reused
	r.TLSHandshakes = tr.tls
	r.ActiveAfterClose = tr.active
	tr.mu.Unlock()
	if e == nil && (r.ActiveAfterClose != 0 || r.PartialsAfterClose != 0) {
		e = errors.New("resources remain after close")
	}
	if e != nil {
		r.OK = false
		r.Error = e.Error()
	}
	// Origin stats and a second, independent on-disk hash run outside CPU markers.
	if controlErr := followControl(control, "/stats", &r.OriginStats); controlErr != nil && e == nil {
		e = controlErr
		r.OK = false
		r.Error = e.Error()
	}
	if p.Mode == "engine" && r.OK && p.Scenario != "cancel" {
		count := 1
		if p.Scenario == "compete" {
			count = 3
		}
		for seed := 0; seed < count; seed++ {
			u, _ := http.NewRequest("GET", followURL(seed, p.Blocks), nil)
			key := u.URL.Query().Get("e")
			path := filepath.Join(p.Storage, "videos", key+".mp4")
			f, err := os.Open(path)
			if err == nil {
				h := md5.New()
				var n int64
				n, err = io.Copy(h, f)
				f.Close()
				if err == nil && (n != int64(p.Blocks)*(1<<20) || fmt.Sprintf("%x", h.Sum(nil)) != key) {
					err = errors.New("published hash mismatch")
				}
			}
			if err != nil {
				r.OK = false
				r.Error = err.Error()
				e = err
				break
			}
		}
	}
	if saveErr := followSave(p.Out, r); saveErr != nil {
		return saveErr
	}
	return e
}

func TestCPUFollowup(t *testing.T) {
	path := os.Getenv("SWD_CPU_FOLLOWUP_PLAN")
	if path == "" {
		t.Skip("opt-in bounded development experiment")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var p followPlan
	if e = json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	if p.Mode == "origin" {
		if p.RateMiB < 4 || p.RateMiB > 32 {
			t.Fatal("rate bound")
		}
		e = followServe(p)
	} else {
		if (p.Mode != "raw" && p.Mode != "engine" && p.Mode != "idle") || p.N < 1 || p.N > 32 || p.Blocks < 4 || p.Blocks > 128 || p.Out == "" {
			t.Fatal("trial bounds")
		}
		e = followRun(p)
	}
	if e != nil {
		t.Fatal(e)
	}
}
