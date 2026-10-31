package console

import (
	"context"

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Both triggers receive the same active access policy. Direct paths retain the
// existing fallback; proxy requests receive only the configured proxy client.
func (c *Console) connectivityAccess() upstreamcheck.ConnectivityAccess {
	p := upstreamcheck.DefaultPolicy()
	return upstreamcheck.ConnectivityAccess{
		Catalog: func(ctx context.Context, route string) (upstreamcheck.Result, int64, upstreamcheck.CatalogResponse) {
			ctx, cancel := context.WithTimeout(ctx, p.RequestTimeout)
			defer cancel()
			clients := c.operationClients(ctx, upstreamstate.Catalog, upstreamstate.Constraints{Entry: catalogmeta.SourceURL(route)})
			return upstreamcheck.ProbeCatalog(ctx, clients, p, route)
		},
		Playback: func(ctx context.Context, id int64, route playbacksource.Entry) (upstreamcheck.Result, *upstreamcheck.Sample, upstreamcheck.PlaybackURLResponse) {
			ctx, cancel := context.WithTimeout(ctx, p.RequestTimeout)
			defer cancel()
			clients := c.operationClients(ctx, upstreamstate.PlaybackURL, upstreamstate.Constraints{Entry: route.Endpoint(playbacksource.DefaultBaseURL)})
			return upstreamcheck.ProbePlayback(ctx, clients, p, id, route)
		},
		Resource: func(ctx context.Context, id int64, sample upstreamcheck.Sample) upstreamcheck.Result {
			ctx, cancel := context.WithTimeout(ctx, p.RequestTimeout+p.ResourceTimeout)
			defer cancel()
			clients := c.operationClients(ctx, upstreamstate.Resource, upstreamstate.Constraints{Entry: upstreamrequest.ResourceScheme + "://" + sample.Host, Target: sample.URL})
			return upstreamcheck.ProbeResourcePaths(ctx, clients, p, id, sample)
		},
	}
}
