package console

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// The application adapter supplies connection choices and records evidence.
// Source eligibility and playback protocol belong to playbacksource. Neither
// batch preparation nor the connection pool selects a CDN.
func (c *Console) resolveSongURL(ctx context.Context, id int64) (string, error) {
	c.mu.Lock()
	engine := c.service
	c.mu.Unlock()
	if engine != nil {
		if err := engine.StorageError(); err != nil {
			return "", err
		}
	}
	revision := c.resources.Revision()
	resolver := playbacksource.Resolver{
		BaseURL: c.apiBase, MaxFileBytes: cacheproxy.DefaultConfig().MaxFileBytes,
		AttemptTimeout: 10 * time.Second,
		Clients: func(ctx context.Context, entry string) []*http.Client {
			return c.operationClients(ctx, upstreamstate.PlaybackURL, upstreamstate.Constraints{Entry: entry})
		},
	}
	address, err := resolver.Resolve(ctx, id)
	if err != nil {
		return "", applog.SafeError(err)
	}
	c.resources.ObserveResourceSourcesAtRevision(revision, "playback/"+address.Node+"/"+strconv.FormatInt(id, 10), []upstreamrequest.ResourceSource{{
		API:  address.API + "?node=" + address.Node + "&id=" + strconv.FormatInt(id, 10),
		Node: address.Node, SongID: id, ResourceURL: address.URL, ObservedAt: address.ObservedAt,
	}}, 10*time.Minute)
	c.mu.Lock()
	engine = c.service
	c.mu.Unlock()
	if engine != nil {
		if err := engine.ObserveSongURL(ctx, cacheproxy.SongURL{SongID: id, URL: address.URL, API: address.API, Node: address.Node, QueryStartedAt: address.QueryStartedAt, ObservedAt: address.ObservedAt}); err != nil {
			return "", fmt.Errorf("保存歌曲地址：%w", err)
		}
	}
	return address.URL, nil
}

func (c *Console) resolvePlayback(ctx context.Context, id string) (string, error) {
	songID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || songID <= 0 {
		return "", fmt.Errorf("invalid song ID: %s", id)
	}
	attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.resolveSongURL(attempt, songID)
}
