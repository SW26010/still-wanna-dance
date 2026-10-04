package upstreamstate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync"
	"time"

	"still-wanna-dance/internal/videometa"
)

const MaxCatalogBytes int64 = 16 << 20

type videoSample struct {
	url, host, checksum string
	size                int64
}

func parseSample(target string) (videoSample, error) {
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Fragment != "" || !allowedVideoHost(u.Host) || (u.Scheme != "https" && u.Scheme != "http") {
		return videoSample{}, errors.New("unsupported video URL")
	}
	meta, err := videometa.Parse(u, 2<<30)
	if err != nil {
		return videoSample{}, err
	}
	u.Scheme = "https"
	return videoSample{u.String(), u.Host, meta.Checksum, meta.Size}, nil
}

type timing struct {
	mu    sync.Mutex
	stage string
	first time.Time
}

type timingSnapshot struct {
	finished time.Time
	first    time.Time
	stage    string
}

func (t *timing) set(s string) { t.mu.Lock(); t.stage = s; t.mu.Unlock() }
func (t *timing) snapshot() timingSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return timingSnapshot{finished: time.Now(), first: t.first, stage: t.stage}
}

// The probe supplies its own start boundary: one API request or a resource chain.
func (o *observation) finishTiming(started time.Time, s timingSnapshot) {
	o.at = s.finished
	o.duration = s.finished.Sub(started)
	o.stage = s.stage
	if !s.first.IsZero() {
		o.latency = s.first.Sub(started)
	}
}
func doProbeRequest(ctx context.Context, client *http.Client, target, byteRange string) (*http.Response, *timing, error) {
	t := &timing{stage: "connect"}
	trace := &httptrace.ClientTrace{TLSHandshakeStart: func() { t.set("tls") }, TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
		if err == nil {
			t.set("headers")
		}
	}, GotConn: func(httptrace.GotConnInfo) { t.set("headers") }, GotFirstResponseByte: func() {
		t.mu.Lock()
		if t.first.IsZero() {
			t.first = time.Now()
		}
		t.mu.Unlock()
	}}
	r, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, target, nil)
	if err != nil {
		return nil, t, err
	}
	if byteRange != "" {
		r.Header.Set("Range", byteRange)
		r.Header.Set("Accept-Encoding", "identity")
	}
	resp, err := client.Do(r)
	if err == nil {
		t.mu.Lock()
		if t.first.IsZero() {
			t.first = time.Now()
		}
		t.stage = "headers"
		t.mu.Unlock()
	}
	return resp, t, err
}
func errorState(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "network_error"
}
func httpState(code int) string {
	if code == 524 {
		return "origin_timeout"
	}
	if code >= 500 {
		return "upstream_error"
	}
	if code == 401 || code == 403 || code == 429 {
		return "restricted"
	}
	return "http_error"
}

func probeCatalog(parent context.Context, client *http.Client, p Policy) (o observation, id int64) {
	o = observation{op: Catalog, route: "api"}
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout)
	defer cancel()
	started := time.Now()
	resp, t, err := doProbeRequest(ctx, client, entry(Catalog, "api"), "")
	defer func() { o.finishTiming(started, t.snapshot()) }()
	if err != nil {
		o.state = errorState(err)
		return
	}
	defer resp.Body.Close()
	o.http = resp.StatusCode
	if resp.StatusCode != 200 {
		o.state = httpState(resp.StatusCode)
		return
	}
	t.set("body")
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxCatalogBytes+1))
	if err != nil {
		o.state = errorState(err)
		return
	}
	if int64(len(body)) > MaxCatalogBytes {
		o.state = "invalid"
		return
	}
	var v struct {
		Groups struct {
			Contents []struct {
				SongInfos []struct {
					ID int64 `json:"id"`
				} `json:"songInfos"`
			} `json:"contents"`
		} `json:"groups"`
	}
	if json.Unmarshal(body, &v) != nil {
		o.state = "invalid"
		return
	}
	seen := make(map[int64]bool)
	for _, g := range v.Groups.Contents {
		for _, s := range g.SongInfos {
			if s.ID > 0 && !seen[s.ID] {
				seen[s.ID] = true
				// Reservoir sampling is uniform over distinct valid song IDs.
				if rand.IntN(len(seen)) == 0 {
					id = s.ID
				}
			}
		}
	}
	if id == 0 {
		o.state = "invalid"
		return
	}
	o.state = "available"
	o.bytes = int64(len(body))
	return
}

func probePlayback(parent context.Context, client *http.Client, p Policy, id int64, r route) (o observation, sample *videoSample) {
	o = observation{op: PlaybackURL, route: r.id, songID: id}
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout)
	defer cancel()
	started := time.Now()
	resp, t, err := doProbeRequest(ctx, client, entry(PlaybackURL, r.id)+"&id="+strconv.FormatInt(id, 10), "")
	defer func() { o.finishTiming(started, t.snapshot()) }()
	if err != nil {
		o.state = errorState(err)
		return
	}
	defer resp.Body.Close()
	o.http = resp.StatusCode
	if resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 307 && resp.StatusCode != 308 {
		o.state = httpState(resp.StatusCode)
		return
	}
	u, err := resp.Location()
	if err != nil {
		o.state = "invalid"
		return
	}
	s, err := parseSample(u.String())
	if err != nil || s.host != r.host {
		o.state = "invalid"
		return
	}
	o.state = "available"
	return o, &s
}

func probeResource(parent context.Context, client *http.Client, p Policy, id int64, r route, s videoSample) (o observation) {
	o = observation{op: Resource, route: r.id, songID: id}
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout+p.ResourceTimeout)
	defer cancel()
	started := time.Now()
	var last *timing
	var finalResponse bool
	defer func() {
		snapshot := last.snapshot()
		if !finalResponse {
			// A rejected redirect or transport failure has no final resource
			// response. Do not report a redirect's first byte as its latency.
			snapshot.first = time.Time{}
		}
		o.finishTiming(started, snapshot)
	}()
	n := s.size
	u := s.url
	for redirects := 0; redirects <= 3; redirects++ {
		resp, t, err := doProbeRequest(ctx, client, u, fmt.Sprintf("bytes=0-%d", n-1))
		last = t
		if err != nil {
			o.state = errorState(err)
			return
		}
		o.http = resp.StatusCode
		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
			resp.Body.Close()
			next, err := resp.Location()
			if err != nil {
				o.state = "invalid"
				return
			}
			v, err := parseSample(next.String())
			if err != nil || v.host != s.host || v.size != s.size || v.checksum != s.checksum {
				o.state = "invalid"
				return
			}
			u = v.url
			continue
		}
		// Resource latency runs from the initial request to the final resource
		// response's first byte, including all preceding redirect requests.
		finalResponse = true
		if resp.StatusCode != 206 {
			resp.Body.Close()
			o.state = httpState(resp.StatusCode)
			return
		}
		if resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-%d/%d", n-1, s.size) || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
			resp.Body.Close()
			o.state = "invalid"
			return
		}
		t.set("body")
		o.bytes, o.transferDuration, err = readResourceSample(ctx, resp.Body, n, p.ResourceMinBytes, p.ResourceMinDuration, p.ResourceTimeout)
		if err != nil {
			o.state = errorState(err)
			return
		}
		o.state = "available"
		return
	}
	o.state = "invalid"
	return
}
