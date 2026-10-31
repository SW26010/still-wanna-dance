package upstreamcheck

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

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/videometa"
)

const MaxCatalogBytes int64 = 16 << 20

type videoSample struct {
	url, host, checksum string
	size                int64
}

func parseSample(target string) (videoSample, error) {
	u, err := url.Parse(target)
	if err != nil {
		return videoSample{}, errors.New("unsupported video URL")
	}
	meta, err := videometa.ValidateURL(u, 2<<30)
	if err != nil {
		return videoSample{}, err
	}
	u.Scheme = "https"
	return videoSample{u.String(), u.Host, meta.Checksum, meta.Size}, nil
}

type timing struct {
	mu     sync.Mutex
	stage  string
	scheme string
	first  time.Time
}

type timingSnapshot struct {
	finished time.Time
	first    time.Time
	stage    string
	scheme   string
}

func (t *timing) set(s string) { t.mu.Lock(); t.stage = s; t.mu.Unlock() }
func (t *timing) snapshot() timingSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return timingSnapshot{finished: time.Now(), first: t.first, stage: t.stage, scheme: t.scheme}
}

// The probe supplies its own start boundary: one API request or a resource chain.
func (o *observation) finishTiming(started time.Time, s timingSnapshot) {
	o.at = s.finished
	o.duration = s.finished.Sub(started)
	o.stage = s.stage
	o.scheme = s.scheme
	o.responded = !s.first.IsZero()
	if !s.first.IsZero() {
		o.latency = s.first.Sub(started)
	}
}
func doProbeRequest(ctx context.Context, client *http.Client, target, byteRange string) (*http.Response, *timing, error) {
	u, _ := url.Parse(target)
	t := &timing{stage: "connect", scheme: u.Scheme}
	trace := &httptrace.ClientTrace{TLSHandshakeStart: func() { t.set("tls") }, TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
		if err == nil {
			t.set("headers")
		}
	}, GotConn: func(httptrace.GotConnInfo) { t.set("headers") }, GotFirstResponseByte: func() {
		t.mu.Lock()
		t.first = time.Now()
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
	resp, err := upstreamrequest.Do(client, r)
	if err == nil {
		t.mu.Lock()
		if t.first.IsZero() {
			t.first = time.Now()
		}
		t.stage = "headers"
		if resp.Request != nil {
			t.scheme = resp.Request.URL.Scheme
		}
		t.mu.Unlock()
	}
	return resp, t, err
}

// Probes use the same complete-attempt policy as real requests, without a
// separate HTTP/HTTPS measurement matrix or a background protocol cache.
func probeProtocols(parent context.Context, session *upstreamrequest.Session, timeout time.Duration, target string, run func(context.Context, *http.Client, string) observation) observation {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	started := time.Now()
	var last observation
	err := session.Read(ctx, target, timeout,
		func(ctx context.Context, client *http.Client, target string) error {
			offset := time.Since(started)
			last = run(ctx, client, target)
			if last.err != nil {
				last.err = upstreamrequest.AttemptError(ctx, last.err)
				last.state = errorState(last.err)
			}
			if last.responded {
				last.latency += offset
			}
			if last.state == "available" || last.state == "redirect" {
				return nil
			}
			if last.err != nil {
				return last.err
			}
			return errors.New(last.state)
		})
	if last.state == "" {
		last.state, last.err, last.at = errorState(err), err, time.Now()
	}
	last.duration = time.Since(started)
	return last
}
func errorState(err error) string {
	if errors.Is(err, upstreamrequest.ErrCapacity) {
		return "local_capacity"
	}
	if errors.Is(err, upstreamrequest.ErrRetired) {
		return "runtime_retired"
	}
	if errors.Is(err, upstreamrequest.ErrUnavailable) {
		return "channel_unavailable"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "network_error"
}

func localObservation(state string) bool {
	return state == "canceled" || state == "local_capacity" || state == "runtime_retired" || state == "channel_unavailable"
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
	o, id, _ = probeCatalogData(parent, []*http.Client{client}, p, route)
	return
}

// The diagnostic can select several IDs from this one validated response.
// Business consumers validate full song metadata before accepting it.
func probeCatalogData(parent context.Context, clients []*http.Client, p Policy, route string) (o observation, id int64, result CatalogResponse) {
	o = probeProtocols(parent, upstreamrequest.NewSession(clients), p.RequestTimeout, entry(Catalog, route), func(ctx context.Context, client *http.Client, target string) observation {
		var current observation
		current, id, result = probeCatalogDataAt(ctx, client, p, route, target)
		return current
	})
	o.op, o.route = Catalog, route
	return
}

func probeCatalogDataAt(parent context.Context, client *http.Client, p Policy, route, target string) (o observation, id int64, result CatalogResponse) {
	defer func() {
		if o.state != "available" {
			result = CatalogResponse{}
		}
	}()
	o = observation{op: Catalog, route: route}
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout)
	defer cancel()
	started := time.Now()
	resp, t, err := doProbeRequest(ctx, client, target, "")
	defer func() { o.finishTiming(started, t.snapshot()) }()
	if err != nil {
		o.state, o.err = errorState(err), err
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
		o.state, o.err = errorState(err), err
		return
	}
	if int64(len(body)) > MaxCatalogBytes {
		o.state = "invalid"
		return
	}
	result = CatalogResponse{Route: route, Source: entry(Catalog, route), Body: body}
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
	c, err := catalogmeta.Parse(body)
	if err != nil || c.Udon {
		return 0, c.Revision
	}
	return c.Entries[rand.IntN(len(c.Entries))].ID, c.Revision
}
func probePlayback(parent context.Context, client *http.Client, p Policy, id int64, r route, alternatives ...*http.Client) (o observation, sample *videoSample) {
	o, sample, _ = probePlaybackResponse(parent, client, p, id, r, alternatives...)
	return
}

func probePlaybackResponse(parent context.Context, client *http.Client, p Policy, id int64, r route, alternatives ...*http.Client) (o observation, sample *videoSample, result PlaybackURLResponse) {
	return probePlaybackSession(parent, upstreamrequest.NewSession(append([]*http.Client{client}, alternatives...)), p, id, r)
}

func probePlaybackSession(parent context.Context, session *upstreamrequest.Session, p Policy, id int64, r route) (o observation, sample *videoSample, result PlaybackURLResponse) {
	target := entry(PlaybackURL, r.ID) + "&id=" + strconv.FormatInt(id, 10)
	o = probeProtocols(parent, session, p.RequestTimeout, target, func(ctx context.Context, client *http.Client, target string) observation {
		var current observation
		current, sample, result = probePlaybackAt(ctx, client, p, id, r, target)
		return current
	})
	o.op, o.route, o.songID = PlaybackURL, r.ID, id
	return
}

func probePlaybackAt(parent context.Context, client *http.Client, p Policy, id int64, r route, target string) (o observation, sample *videoSample, result PlaybackURLResponse) {
	o = observation{op: PlaybackURL, route: r.ID, songID: id}
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout)
	defer cancel()
	started := time.Now()
	resp, t, err := doProbeRequest(ctx, client, target, "")
	defer func() {
		o.finishTiming(started, t.snapshot())
		if o.state == "available" {
			result.QueryStartedAt, result.ObservedAt = started, o.at
		}
	}()
	if err != nil {
		o.state, o.err = errorState(err), err
		return
	}
	defer resp.Body.Close()
	o.http = resp.StatusCode
	if !videometa.IsPlaybackRedirect(resp.StatusCode) {
		o.state = httpState(resp.StatusCode)
		return
	}
	u, meta, err := videometa.PlaybackLocation(resp, 2<<30)
	if err != nil {
		o.state = "invalid"
		return
	}
	resourceURL := *u
	resourceURL.Scheme = "https"
	s := videoSample{resourceURL.String(), u.Host, meta.Checksum, meta.Size}
	o.state = "available"
	o.resourceHost = s.host
	o.resourceSource = upstreamrequest.ResourceSource{API: entry(PlaybackURL, r.ID) + "&id=" + strconv.FormatInt(id, 10), Node: r.Node, SongID: id, ResourceURL: u.String(), Domain: u.Host, Path: u.Path}
	result = PlaybackURLResponse{SongID: id, URL: u.String(), API: apiBase + "/Api/Songs/play", Node: r.Node}
	return o, &s, result
}

func probeResource(parent context.Context, client *http.Client, p Policy, id int64, s videoSample) (o observation) {
	return probeResourceMode(parent, client, p, id, s, true)
}

func probeResourceMode(parent context.Context, client *http.Client, p Policy, id int64, s videoSample, throughput bool) (o observation) {
	return probeResourcePaths(parent, []*http.Client{client}, p, id, s, throughput)
}

func probeResourcePaths(parent context.Context, clients []*http.Client, p Policy, id int64, s videoSample, throughput bool) (o observation) {
	ctx, cancel := context.WithTimeout(parent, p.RequestTimeout+p.ResourceTimeout)
	defer cancel()
	started := time.Now()
	defer func() {
		o.op, o.route, o.songID = Resource, s.host, id
		o.duration = time.Since(started)
	}()
	// Samples and validated redirects normalize to HTTPS. Reject revisits as
	// redirect loops instead of giving the same endpoint a new attempt budget.
	// Exact HTTP-to-HTTPS upgrades stay inside the shared Fetch cursor.
	visited := make(map[string]bool)
	for redirects := 0; redirects <= 3; redirects++ {
		if visited[s.url] {
			o.state, o.latency = "invalid", 0
			return
		}
		visited[s.url] = true
		var next string
		offset := time.Since(started)
		o = probeProtocols(ctx, upstreamrequest.NewSession(clients), p.RequestTimeout+p.ResourceTimeout, s.url, func(ctx context.Context, client *http.Client, target string) observation {
			current := s
			current.url = target
			var result observation
			result, next = probeResourceAt(ctx, client, p, id, current, throughput)
			return result
		})
		if o.state != "redirect" {
			if o.responded {
				o.latency += offset
			}
			return
		}
		s.url = next
	}
	o.state, o.latency = "invalid", 0
	return
}

func probeResourceAt(ctx context.Context, client *http.Client, p Policy, id int64, s videoSample, throughput bool) (o observation, redirect string) {
	o = observation{op: Resource, route: s.host, songID: id}
	started := time.Now()
	n := min(s.size, p.ResourceMaxBytes)
	if !throughput {
		n = 1
	}
	resp, t, err := doProbeRequest(ctx, client, s.url, fmt.Sprintf("bytes=0-%d", n-1))
	var finalResponse bool
	defer func() {
		snapshot := t.snapshot()
		if !finalResponse {
			// A rejected redirect or transport failure has no final resource
			// response. Do not report a redirect's first byte as its latency.
			snapshot.first = time.Time{}
		}
		o.finishTiming(started, snapshot)
	}()
	if err != nil {
		o.state, o.err = errorState(err), err
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
		o.state = "redirect"
		return o, v.url
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
			o.state, o.err = errorState(err), err
			return
		}
		o.state = "available"
		return
	}
	o.bytes, o.transferDuration, err = readResourceSample(ctx, resp.Body, n, p.ResourceMaxBytes, p.ResourceTimeout)
	if err != nil {
		o.state, o.err = errorState(err), err
		return
	}
	o.state = "available"
	return
}
