package console

import (
	"context"
	"log/slog"
	"net/url"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func (c *Console) connectivityCachedSong(ctx context.Context, previous int64) int64 {
	store, err := c.businessStore()
	var id int64
	if err == nil {
		id, err = store.CatalogSampleSong(ctx, previous)
	}
	if err != nil && ctx.Err() == nil {
		slog.Warn("连通性检测的清单样本读取失败", "event_name", "connectivity_catalog_sample_failed", "error", applog.SafeError(err))
	}
	return id
}

func (c *Console) connectivityCachedResource(ctx context.Context) string {
	return c.cachedResourceForHost(ctx, "", cacheproxy.MediaURL{}).URL
}

func (c *Console) cachedResourceForHost(ctx context.Context, host string, after cacheproxy.MediaURL) cacheproxy.MediaURL {
	store, err := c.businessStore()
	if err == nil {
		var ok bool
		var sample cacheproxy.MediaURL
		sample, ok, err = store.NextAvailableMediaURL(ctx, after, func(target string) bool {
			if host != "" {
				u, err := url.Parse(target)
				if err != nil || u.Hostname() != host {
					return false
				}
			}
			return upstreamcheck.ValidResourceSampleURL(target)
		})
		if ok {
			return sample
		}
	}
	if err != nil && ctx.Err() == nil {
		slog.Warn("连通性检测的资源样本读取失败", "event_name", "connectivity_resource_sample_failed", "error", applog.SafeError(err))
	}
	return cacheproxy.MediaURL{}
}

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
