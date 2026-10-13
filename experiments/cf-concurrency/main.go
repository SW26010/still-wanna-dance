// Development-only CF Range experiment. No product settings or caches are used.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/videometa"
)

const blockSize int64 = 1 << 20

type target struct {
	URL  string `json:"url"` // Private input only; never copied into observations.
	Host string `json:"host"`
	IP   string `json:"ip"`
	Song int    `json:"song"`
	Size int64  `json:"size"`
	ETag string `json:"etag"`
}

type pinnedDNS struct{ host, ip string }

func (d pinnedDNS) Addresses(context.Context, string) ([]upstreamrequest.Address, error) {
	return d.CurrentAddresses(d.host), nil
}
func (d pinnedDNS) CurrentAddresses(host string) []upstreamrequest.Address {
	if host != d.host {
		return nil
	}
	return []upstreamrequest.Address{{IP: d.ip, Expires: time.Now().Add(time.Hour)}}
}
func (pinnedDNS) DNSChanged() <-chan struct{} { return nil }

func clientFor(t target) (*http.Client, func(), error) {
	p, err := upstreamrequest.NewPool("direct", pinnedDNS{t.Host, t.IP}, nil)
	if err != nil {
		return nil, nil, err
	}
	cs, err := p.Candidates(context.Background(), t.URL)
	if err != nil || len(cs) != 1 {
		p.Close()
		return nil, nil, errors.New("missing fixed direct candidate")
	}
	return &http.Client{Transport: cs[0].Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, p.Close, nil
}

func prepare(ctx context.Context, song int) (target, error) {
	tr := &http.Transport{DialContext: console.NewUpstreamDialer(), TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.udon.dance/Api/Songs/play?node=cf&id=%d", song), nil)
	resp, err := c.Do(r)
	if err != nil {
		return target{}, err
	}
	resp.Body.Close()
	if resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 307 && resp.StatusCode != 308 {
		return target{}, fmt.Errorf("API status %d", resp.StatusCode)
	}
	u, err := resp.Location()
	if err != nil {
		return target{}, err
	}
	if u.User != nil || u.Fragment != "" || !videometa.ValidHost(u.Host) || (u.Scheme != "http" && u.Scheme != "https") {
		return target{}, errors.New("invalid API resource")
	}
	m, err := videometa.Parse(u, 2<<30)
	if err != nil {
		return target{}, err
	}
	u.Scheme = "https"
	t := target{URL: u.String(), Host: u.Hostname(), Song: song, Size: m.Size}
	// Discover the connected address using the application's independent DNS,
	// then pin every measurement to it. TLS still verifies the public hostname.
	var peer string
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { peer = info.Conn.RemoteAddr().String() }}
	r, _ = http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", t.URL, nil)
	r.Header.Set("Range", "bytes=0-1023")
	r.Header.Set("Accept-Encoding", "identity")
	resp, err = c.Do(r)
	if err != nil {
		return target{}, err
	}
	defer resp.Body.Close()
	if err = validateResponse(resp, 0, 1024, t.Size, ""); err != nil {
		return target{}, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil || len(data) != 1024 {
		return target{}, errors.New("preflight body incomplete")
	}
	t.IP, _, err = net.SplitHostPort(peer)
	if err != nil {
		return target{}, err
	}
	t.ETag = resp.Header.Get("ETag")
	fmt.Printf("preflight song=%d host=%s ip=%s size=%d protocol=%s server=%s cache=%s colo=%s\n", song, t.Host, t.IP, t.Size, resp.Proto, resp.Header.Get("Server"), resp.Header.Get("CF-Cache-Status"), colo(resp.Header.Get("CF-Ray")))
	return t, nil
}

func validateResponse(r *http.Response, start, length, total int64, etag string) error {
	if r.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP status %d", r.StatusCode)
	}
	if r.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, total) {
		return errors.New("Content-Range mismatch")
	}
	if r.ContentLength >= 0 && r.ContentLength != length {
		return errors.New("Content-Length mismatch")
	}
	if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return errors.New("encoded response")
	}
	if etag != "" && !strings.HasPrefix(etag, "W/") && r.Header.Get("ETag") != etag {
		return errors.New("entity changed")
	}
	return nil
}

type observation struct {
	Offset      int64    `json:"offset"`
	Length      int64    `json:"length"`
	Bytes       int64    `json:"bytes"`
	StartMS     float64  `json:"startMS"`
	ElapsedMS   float64  `json:"elapsedMS"`
	HeadersMS   float64  `json:"headersMS"`
	FirstBodyMS float64  `json:"firstBodyMS"`
	Status      int      `json:"status"`
	Protocol    string   `json:"protocol"`
	Connection  int      `json:"connection"`
	Reused      bool     `json:"reused"`
	Remote      string   `json:"remote"`
	Cache       string   `json:"cache"`
	Age         string   `json:"age,omitempty"`
	Colo        string   `json:"colo,omitempty"`
	Server      string   `json:"server,omitempty"`
	Hashes      []string `json:"hashes,omitempty"`
	Error       string   `json:"error,omitempty"`
}
type runResult struct {
	Round       int           `json:"round"`
	Arm         string        `json:"arm"`
	Concurrency int           `json:"concurrency"`
	StartMS     float64       `json:"startMS"`
	ElapsedMS   float64       `json:"elapsedMS"`
	Bytes       int64         `json:"bytes"`
	Peak        int           `json:"peakRequests"`
	MeanActive  float64       `json:"meanActiveRequests"`
	MiBPerSec   float64       `json:"mibPerSec"`
	OK          bool          `json:"ok"`
	Error       string        `json:"error,omitempty"`
	Requests    []observation `json:"requests"`
}
type traceState struct {
	mu           sync.Mutex
	connections  map[net.Conn]int
	active, peak int
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func colo(ray string) string {
	if i := strings.LastIndexByte(ray, '-'); i >= 0 {
		return ray[i+1:]
	}
	return ""
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return applog.SafeError(err).Error()
}

func fetch(ctx context.Context, c *http.Client, t target, start, length int64, zero time.Time, state *traceState, reference []string) (o observation) {
	begin := time.Now()
	o.Offset, o.Length, o.StartMS = start, length, ms(begin.Sub(zero))
	state.mu.Lock()
	state.active++
	state.peak = max(state.peak, state.active)
	state.mu.Unlock()
	defer func() { o.ElapsedMS = ms(time.Since(begin)); state.mu.Lock(); state.active--; state.mu.Unlock() }()
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		state.mu.Lock()
		defer state.mu.Unlock()
		id := state.connections[info.Conn]
		if id == 0 {
			id = len(state.connections) + 1
			state.connections[info.Conn] = id
		}
		o.Connection, o.Reused, o.Remote = id, info.Reused, info.Conn.RemoteAddr().String()
	}}
	r, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", t.URL, nil)
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
	r.Header.Set("Accept-Encoding", "identity")
	if t.ETag != "" && !strings.HasPrefix(t.ETag, "W/") {
		r.Header.Set("If-Match", t.ETag)
	}
	resp, err := c.Do(r)
	o.HeadersMS = ms(time.Since(begin))
	if err != nil {
		o.Error = safeError(err)
		return
	}
	defer resp.Body.Close()
	o.Status, o.Protocol = resp.StatusCode, resp.Proto
	o.Cache, o.Age, o.Colo, o.Server = resp.Header.Get("CF-Cache-Status"), resp.Header.Get("Age"), colo(resp.Header.Get("CF-Ray")), resp.Header.Get("Server")
	if err = validateResponse(resp, start, length, t.Size, t.ETag); err != nil {
		o.Error = safeError(err)
		return
	}
	buf := make([]byte, blockSize)
	for remaining := length; remaining > 0; {
		want := min(int64(len(buf)), remaining)
		// Measure the first readable body byte separately from response headers.
		if o.Bytes == 0 {
			n, e := io.ReadFull(resp.Body, buf[:1])
			o.Bytes += int64(n)
			o.FirstBodyMS = ms(time.Since(begin))
			if e != nil {
				o.Error = safeError(e)
				return
			}
			n, e = io.ReadFull(resp.Body, buf[1:want])
			o.Bytes += int64(n)
			if e != nil {
				o.Error = safeError(e)
				return
			}
		} else {
			n, e := io.ReadFull(resp.Body, buf[:want])
			o.Bytes += int64(n)
			if e != nil {
				o.Error = safeError(e)
				return
			}
		}
		sum := sha256.Sum256(buf[:want])
		hash := hex.EncodeToString(sum[:])
		o.Hashes = append(o.Hashes, hash)
		index := (start + length - remaining) / blockSize
		if reference != nil && (index >= int64(len(reference)) || reference[index] != hash) {
			o.Error = "block SHA256 mismatch"
			return
		}
		remaining -= want
	}
	var extra [1]byte
	n, e := resp.Body.Read(extra[:])
	o.Bytes += int64(n)
	if n != 0 || e != io.EOF {
		o.Error = "response did not end at advertised length"
	}
	return
}

func measure(ctx context.Context, t target, size int64, n int, reference []string, zero time.Time) runResult {
	result := runResult{Concurrency: max(n, 1), Arm: fmt.Sprintf("range-%d", n)}
	if n == 0 {
		result.Arm = "long"
	}
	c, closeClient, err := clientFor(t)
	if err != nil {
		result.Error = safeError(err)
		return result
	}
	defer closeClient()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &traceState{connections: make(map[net.Conn]int)}
	count := int(size / blockSize)
	if n == 0 {
		count = 1
	}
	result.Requests = make([]observation, count)
	var next atomic.Int64
	begin := time.Now()
	result.StartMS = ms(begin.Sub(zero))
	var workers sync.WaitGroup
	for range max(n, 1) {
		workers.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= count || ctx.Err() != nil {
					return
				}
				start, length := int64(i)*blockSize, blockSize
				if n == 0 {
					start, length = 0, size
				}
				o := fetch(ctx, c, t, start, length, begin, state, reference)
				result.Requests[i] = o
				if o.Error != "" {
					cancel()
					return
				}
			}
		})
	}
	workers.Wait()
	result.ElapsedMS = ms(time.Since(begin))
	result.Peak = state.peak
	result.OK = true
	for _, o := range result.Requests {
		result.Bytes += o.Bytes
		result.MeanActive += o.ElapsedMS
		if o.Error != "" || o.Length == 0 {
			result.OK = false
			if result.Error == "" {
				result.Error = o.Error
				if o.Length == 0 {
					result.Error = "unstarted blocks"
				}
			}
		}
	}
	result.MeanActive /= result.ElapsedMS
	if result.OK {
		result.MiBPerSec = float64(size) / float64(blockSize) / (result.ElapsedMS / 1000)
	}
	return result
}

func save(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	e := json.NewEncoder(f)
	e.SetIndent("", "  ")
	return e.Encode(value)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, safeError(err))
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "prepare", "prepare or measure")
	private := flag.String("private", "artifacts/cf-concurrency/target.json", "private target file")
	out := flag.String("out", "artifacts/cf-concurrency/results.json", "measurement output")
	song := flag.Int("song", 1343, "API song ID")
	sizeMiB := flag.Int("mib", 32, "same prefix length for every arm")
	rounds := flag.Int("rounds", 4, "number of balanced-order rows, 1..4 (use 4 for primary comparisons)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *mode == "prepare" {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		t, err := prepare(ctx, *song)
		if err != nil {
			return err
		}
		return save(*private, t)
	}
	if *mode != "measure" {
		return errors.New("unknown mode")
	}
	var t target
	b, err := os.ReadFile(*private)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &t); err != nil {
		return err
	}
	u, err := url.Parse(t.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() != t.Host || net.ParseIP(t.IP) == nil {
		return errors.New("invalid private target")
	}
	if *sizeMiB < 1 || *sizeMiB > 32 {
		return errors.New("measurement size must be 1..32 MiB")
	}
	if *rounds < 1 || *rounds > 4 {
		return errors.New("measurement rounds must be 1..4")
	}
	size := int64(*sizeMiB) * blockSize
	if size > t.Size {
		return errors.New("resource smaller than requested interval")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	zero := time.Now()
	report := struct {
		Source   string      `json:"source"`
		Host     string      `json:"host"`
		IP       string      `json:"ip"`
		Song     int         `json:"song"`
		Size     int64       `json:"resourceBytes"`
		Interval int64       `json:"intervalBytes"`
		Method   string      `json:"method"`
		Runs     []runResult `json:"runs"`
	}{Source: "node=cf", Host: t.Host, IP: t.IP, Song: t.Song, Size: t.Size, Interval: size, Method: "pinned direct product candidate transport; fresh connection pool per run; 1 MiB ranges; no explicit retries; SHA256 compared to warmup; not whole-cache-engine or game measurement"}
	execute := func(round, n int) runResult {
		trial, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		var reference []string
		if len(report.Runs) > 0 {
			reference = report.Runs[0].Requests[0].Hashes
		}
		r := measure(trial, t, size, n, reference, zero)
		r.Round = round
		report.Runs = append(report.Runs, r)
		fmt.Printf("round=%d arm=%s ok=%v seconds=%.3f MiB/s=%.3f peak=%d mean_active=%.2f bytes=%d\n", round, r.Arm, r.OK, r.ElapsedMS/1000, r.MiBPerSec, r.Peak, r.MeanActive, r.Bytes)
		return r
	}
	warm := execute(0, 0)
	if err = save(*out, report); err != nil {
		return err
	}
	if !warm.OK {
		return errors.New("warmup failed; measurements not started")
	}
	orders := [][]int{{0, 1, 2, 4, 3}, {2, 0, 3, 1, 4}, {3, 4, 2, 0, 1}, {4, 1, 3, 2, 0}}
	consecutiveFailures := 0
	for round, order := range orders[:*rounds] {
		for _, n := range order {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			r := execute(round+1, n)
			if err = save(*out, report); err != nil {
				return err
			}
			if r.OK {
				consecutiveFailures = 0
			} else {
				consecutiveFailures++
			}
			for _, o := range r.Requests {
				if o.Status == 429 || o.Status == 503 {
					return errors.New("upstream throttling/service-unavailable response; stopped")
				}
			}
			if consecutiveFailures >= 2 {
				return errors.New("two consecutive failed arms; stopped")
			}
		}
	}
	return nil
}
