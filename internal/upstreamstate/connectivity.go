package upstreamstate

import (
	"context"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// ConnectivityResult is a single step of the wizard's manual check. Resource
// probing reuses the monitor's one-byte mode; it never measures throughput.
type ConnectivityResult struct {
	Operation Operation `json:"operation"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	LatencyMS *float64  `json:"latencyMS"`
}

func CheckConnectivity(ctx context.Context, channel *upstreamrequest.Channel, upstream string, update func(ConnectivityResult)) {
	source := channel.Snapshot()
	p := DefaultPolicy()
	p.RequestTimeout = 10 * time.Second
	publish := func(op Operation, o observation) {
		if ctx.Err() != nil {
			return
		}
		r := ConnectivityResult{Operation: op, State: "failed", Reason: o.state}
		if o.state == "available" {
			r.State, r.Reason = "available", ""
		}
		if o.latency > 0 || o.state == "available" {
			ms := float64(o.latency) / float64(time.Millisecond)
			r.LatencyMS = &ms
		}
		update(r)
	}
	var song int64
	var catalog observation
	update(ConnectivityResult{Operation: Catalog, State: "checking"})
	for _, routeID := range routeIDs(Catalog) {
		if ctx.Err() != nil {
			return
		}
		client, err := calibrationClient(ctx, source, entry(Catalog, routeID), upstream)
		if err != nil {
			catalog.state = "channel_unavailable"
			continue
		}
		catalog, song = probeCatalogRoute(ctx, client, p, routeID)
		client.CloseIdleConnections()
		if catalog.state == "available" {
			break
		}
	}
	publish(Catalog, catalog)
	if ctx.Err() != nil {
		return
	}
	if song == 0 {
		update(ConnectivityResult{Operation: PlaybackURL, State: "skipped", Reason: "no_sample"})
		update(ConnectivityResult{Operation: Resource, State: "skipped", Reason: "no_sample"})
		return
	}
	update(ConnectivityResult{Operation: PlaybackURL, State: "checking"})
	client, err := calibrationClient(ctx, source, entry(PlaybackURL, "cf"), upstream)
	if err != nil {
		publish(PlaybackURL, observation{state: "channel_unavailable"})
		update(ConnectivityResult{Operation: Resource, State: "skipped", Reason: "no_sample"})
		return
	}
	playback, sample := probePlayback(ctx, client, p, song, route{id: "cf", node: "cf"})
	client.CloseIdleConnections()
	publish(PlaybackURL, playback)
	if ctx.Err() != nil {
		return
	}
	if sample == nil {
		update(ConnectivityResult{Operation: Resource, State: "skipped", Reason: "no_sample"})
		return
	}
	update(ConnectivityResult{Operation: Resource, State: "checking"})
	channel.ObserveResourceDomainsAtRevision(source.Revision, "setup-connectivity", []string{sample.host}, time.Minute)
	defer channel.ObserveResourceDomainsAtRevision(source.Revision, "setup-connectivity", nil, 0)
	client, err = calibrationClient(ctx, source, sample.url, upstream)
	if err != nil {
		publish(Resource, observation{state: "channel_unavailable"})
		return
	}
	defer client.CloseIdleConnections()
	publish(Resource, probeResourceMode(ctx, client, p, song, *sample, false))
}
