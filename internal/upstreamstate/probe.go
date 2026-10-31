package upstreamstate

import (
	"context"
	"net/http"

	"still-wanna-dance/internal/upstreamcheck"
)

type videoSample struct {
	url, host, checksum string
	size                int64
}

func probePolicy(p Policy) upstreamcheck.Policy {
	return upstreamcheck.Policy{RequestTimeout: p.RequestTimeout, ResourceMaxBytes: p.ResourceMaxBytes, ResourceTimeout: p.ResourceTimeout}
}
func observed(r upstreamcheck.Result) observation {
	return observation{op: Operation(r.Operation), route: r.Route, state: r.State, stage: r.Stage, scheme: r.Scheme, err: r.Err, http: r.HTTP, at: r.ObservedAt,
		latency: r.Latency, duration: r.Duration, transferDuration: r.TransferDuration, responded: r.Responded, bytes: r.Bytes, songID: r.SongID,
		catalogTime: r.CatalogTime, resourceHost: r.ResourceHost, resourceSource: r.ResourceSource}
}
func probeCatalogResponse(ctx context.Context, client *http.Client, p Policy, route string) (observation, int64, CatalogResponse) {
	o, id, r := upstreamcheck.ProbeCatalog(ctx, []*http.Client{client}, probePolicy(p), route)
	return observed(o), id, r
}
func probePlaybackResponse(ctx context.Context, client *http.Client, p Policy, id int64, r route) (observation, *videoSample, PlaybackURLResponse) {
	o, s, response := upstreamcheck.ProbePlayback(ctx, []*http.Client{client}, probePolicy(p), id, r)
	if s == nil {
		return observed(o), nil, response
	}
	return observed(o), &videoSample{s.URL, s.Host, s.Checksum, s.Size}, response
}
func probeResourceMode(ctx context.Context, client *http.Client, p Policy, id int64, s videoSample, throughput bool) observation {
	return observed(upstreamcheck.ProbeResource(ctx, client, probePolicy(p), id, upstreamcheck.Sample{URL: s.url, Host: s.host, Checksum: s.checksum, Size: s.size}, throughput))
}
func localObservation(state string) bool {
	return state == "canceled" || state == "local_capacity" || state == "runtime_retired" || state == "channel_unavailable"
}
