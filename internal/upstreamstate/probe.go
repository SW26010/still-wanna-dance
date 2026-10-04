package upstreamstate

import (
	"context"
	"crypto/tls"
	"encoding/hex"
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
	return probeCatalogRoute(parent, client, p, "api")
}

func probeCatalogRoute(parent context.Context, client *http.Client, p Policy, route string) (o observation, id int64) {
	o = observation{op: Catalog, route: route}
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout)
	defer cancel()
	started := time.Now()
	resp, t, err := doProbeRequest(ctx, client, entry(Catalog, route), "")
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
	if route == "kiva" || route == "wanna" {
		id, o.catalogTime = kivaSongSample(body)
		if id == 0 {
			o.state = "invalid"
			return
		}
		o.state, o.bytes = "available", int64(len(body))
		return
	}
	var v struct {
		Time   string `json:"time"`
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
	o.catalogTime = v.Time
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

// Kiva supplies a versioned catalog with resource MD5s, unlike Udon's list.
// Reject invalid or conflicting mappings before using the list as a sample.
func kivaSongSample(body []byte) (int64, string) {
	var v struct {
		Code int `json:"code"`
		Data struct {
			Time   string `json:"time"`
			Groups []struct {
				Entries []struct {
					ID       int64  `json:"id"`
					Checksum string `json:"checksum"`
				} `json:"entries"`
			} `json:"groups"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &v) != nil || v.Code != 200 || v.Data.Time == "" {
		return 0, v.Data.Time
	}
	seen := make(map[int64]string)
	var id int64
	for _, group := range v.Data.Groups {
		for _, song := range group.Entries {
			digest, err := hex.DecodeString(song.Checksum)
			if song.ID <= 0 || err != nil || len(digest) != 16 {
				return 0, v.Data.Time
			}
			checksum := string(digest)
			if previous, ok := seen[song.ID]; ok {
				if previous != checksum {
					return 0, v.Data.Time
				}
				continue
			}
			seen[song.ID] = checksum
			if rand.IntN(len(seen)) == 0 {
				id = song.ID
			}
		}
	}
	return id, v.Data.Time
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
	return probeResourceMode(parent, client, p, id, r, s, true)
}

func probeResourceMode(parent context.Context, client *http.Client, p Policy, id int64, r route, s videoSample, throughput bool) (o observation) {
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
	n := min(s.size, p.ResourceMaxBytes)
	if !throughput {
		n = 1
	}
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
		if resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-%d/%d", n-1, s.size) || (resp.ContentLength > 0 && resp.ContentLength != n) || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
			resp.Body.Close()
			o.state = "invalid"
			return
		}
		t.set("body")
		if !throughput {
			var first [1]byte
			_, err = io.ReadFull(resp.Body, first[:])
			resp.Body.Close()
			if err != nil {
				o.state = errorState(err)
				return
			}
			o.state = "available"
			return
		}
		o.bytes, o.transferDuration, err = readResourceSample(ctx, resp.Body, n, p.ResourceMaxBytes, p.ResourceTimeout)
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
