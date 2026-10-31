// Package upstreamcheck measures requests through caller-supplied HTTP clients.
// It has no candidate discovery, route recommendations, or observation history.
package upstreamcheck

import (
	"context"
	"net/http"
	"time"

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/videometa"
)

type Operation string

const (
	Catalog     Operation = "catalog"
	PlaybackURL Operation = "playback_url"
	Resource    Operation = "resource"
)

type Policy struct {
	RequestTimeout   time.Duration
	ResourceMaxBytes int64
	ResourceTimeout  time.Duration
}

func DefaultPolicy() Policy { return Policy{30 * time.Second, 16 << 20, 3 * time.Second} }

type CatalogResponse struct {
	Route, Source string
	Body          []byte
}
type PlaybackURLResponse struct {
	SongID                     int64
	URL, API, Node             string
	QueryStartedAt, ObservedAt time.Time
}
type Sample struct {
	URL, Host, Checksum string
	Size                int64
}

// Result describes only this request, including its bounded protocol fallback.
type Result struct {
	Operation                           Operation
	Route, State, Stage, Scheme         string
	Err                                 error
	HTTP                                int
	ObservedAt                          time.Time
	Latency, Duration, TransferDuration time.Duration
	Responded                           bool
	Bytes, SongID                       int64
	CatalogTime, ResourceHost           string
	ResourceSource                      upstreamrequest.ResourceSource
}

func (o observation) result() Result {
	return Result{Operation: o.op, Route: o.route, State: o.state, Stage: o.stage, Scheme: o.scheme, Err: o.err,
		HTTP: o.http, ObservedAt: o.at, Latency: o.latency, Duration: o.duration, TransferDuration: o.transferDuration,
		Responded: o.responded, Bytes: o.bytes, SongID: o.songID, CatalogTime: o.catalogTime, ResourceHost: o.resourceHost, ResourceSource: o.resourceSource}
}
func (s videoSample) sample() Sample { return Sample{s.url, s.host, s.checksum, s.size} }

// ProbeCatalog uses the supplied paths within one request budget.
func ProbeCatalog(ctx context.Context, clients []*http.Client, p Policy, route string) (Result, int64, CatalogResponse) {
	o, id, response := probeCatalogData(ctx, clients, p, route)
	return o.result(), id, response
}
func ProbePlayback(ctx context.Context, clients []*http.Client, p Policy, id int64, r playbacksource.Entry) (Result, *Sample, PlaybackURLResponse) {
	o, s, response := probePlaybackSession(ctx, upstreamrequest.NewSession(clients), p, id, r)
	if s == nil {
		return o.result(), nil, response
	}
	sample := s.sample()
	return o.result(), &sample, response
}
func ProbeResource(ctx context.Context, client *http.Client, p Policy, id int64, s Sample, throughput bool) Result {
	return probeResourceMode(ctx, client, p, id, videoSample{s.URL, s.Host, s.Checksum, s.Size}, throughput).result()
}
func ProbeResourcePaths(ctx context.Context, clients []*http.Client, p Policy, id int64, s Sample) Result {
	return probeResourcePaths(ctx, clients, p, id, videoSample{s.URL, s.Host, s.Checksum, s.Size}, false).result()
}
func requestClient(t http.RoundTripper) *http.Client {
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type route = playbacksource.Entry

var playbackRoutes = playbacksource.Entries()

const apiBase = playbacksource.DefaultBaseURL

type observation struct {
	err                 error
	scheme              string
	responded           bool
	resourceSource      upstreamrequest.ResourceSource
	resourceHost        string
	catalogTime         string
	op                  Operation
	route, state, stage string
	http                int
	at                  time.Time
	latency, duration   time.Duration
	transferDuration    time.Duration
	bytes               int64
	songID              int64
}

func routeIDs(op Operation) []string {
	if op == Catalog {
		return catalogmeta.SourceIDs()
	}
	if op == PlaybackURL {
		ids := make([]string, len(playbackRoutes))
		for i, r := range playbackRoutes {
			ids[i] = r.ID
		}
		return ids
	}
	return nil
}
func entry(op Operation, id string) string {
	if op == Catalog {
		return catalogmeta.SourceURL(id)
	}
	if op == Resource && videometa.ValidHost(id) {
		return "https://" + id
	}
	for _, r := range playbackRoutes {
		if r.ID == id {
			if op == PlaybackURL {
				return r.Endpoint(apiBase)
			}
		}
	}
	return ""
}
