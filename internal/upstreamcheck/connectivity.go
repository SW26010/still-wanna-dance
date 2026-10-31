package upstreamcheck

import (
	"context"

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/playbacksource"
)

// ConnectivityAccess is assembled by the application. Each operation owns its
// bounded candidate retries; the check never discovers channels.
type ConnectivityAccess struct {
	Catalog  func(context.Context, string) (Result, int64, CatalogResponse)
	Playback func(context.Context, int64, playbacksource.Entry) (Result, *Sample, PlaybackURLResponse)
	Resource func(context.Context, int64, Sample) Result
}

// ConnectivityBusiness consumes the same responses that produced observations.
// Selection and persistence are business decisions, not network health signals.
type ConnectivityBusiness struct {
	// OnCatalog reports acceptance separately from a local execution failure.
	// Rejected content permits a backup; storage/cancellation errors stop the run.
	OnCatalog     func(context.Context, []CatalogResponse) (bool, error)
	SelectSong    func(context.Context, int64) int64
	OnPlaybackURL func(context.Context, PlaybackURLResponse)
}

// CheckConnectivity is the single automatic/manual lightweight execution flow.
// Initialization retries check catalogs only and publish no other observations.
func CheckConnectivity(ctx context.Context, access ConnectivityAccess, business ConnectivityBusiness, catalogOnly bool, publish func(Result)) {
	checking := func(op Operation, route string) { publish(Result{Operation: op, Route: route, State: "checking"}) }
	var sampleSong int64
	accepted := false
	for _, route := range catalogmeta.SourceIDs() {
		if ctx.Err() != nil {
			return
		}
		if accepted {
			publish(Result{Operation: Catalog, Route: route, State: "skipped"})
			continue
		}
		checking(Catalog, route)
		result, id, response := access.Catalog(ctx, route)
		publish(result)
		if len(response.Body) > 0 {
			var err error
			accepted, err = business.OnCatalog(ctx, []CatalogResponse{response})
			if err != nil {
				return
			}
			if accepted {
				sampleSong = id
			}
		}
	}
	if catalogOnly || ctx.Err() != nil {
		return
	}
	id := business.SelectSong(ctx, sampleSong)
	if id <= 0 {
		for _, op := range []Operation{PlaybackURL, Resource} {
			publish(Result{Operation: op, State: "skipped"})
		}
		return
	}
	seen := map[string]bool{}
	for _, route := range playbacksource.Entries() {
		if ctx.Err() != nil {
			return
		}
		checking(PlaybackURL, route.ID)
		result, sample, response := access.Playback(ctx, id, route)
		publish(result)
		if response.URL != "" && ctx.Err() == nil {
			business.OnPlaybackURL(ctx, response)
		}
		if sample == nil || seen[sample.Host] || ctx.Err() != nil {
			continue
		}
		seen[sample.Host] = true
		checking(Resource, sample.Host)
		publish(access.Resource(ctx, id, *sample))
	}
	if len(seen) == 0 {
		publish(Result{Operation: Resource, State: "skipped"})
	}
}
