package console

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

func (c *Console) metadataSongCandidates(ctx context.Context) []int64 {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	store, storeErr := c.businessStore()
	closing := c.closing
	c.mu.Unlock()
	// Sampling reads the application-owned store without starting a media engine.
	if storeErr != nil || closing || ctx.Err() != nil {
		return nil
	}
	ids, err := store.SongMetadataCandidates(ctx, c.apiBase+"/Api/Songs/play")
	if err != nil && ctx.Err() == nil {
		slog.Warn("metadata_sync_metadata_candidates_failed", "error", applog.SafeError(err))
	}
	return ids
}

// Reuse the successful upstream response without resolving or loading the song.
func (c *Console) syncPlaybackURL(ctx context.Context, response upstreamcheck.PlaybackURLResponse) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.closing || ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	store, err := c.businessStore()
	c.mu.Unlock()
	c.resources.ObserveResourceSourcesAtRevision(c.resources.Revision(), "metadata/"+response.Node+"/"+strconv.FormatInt(response.SongID, 10), []upstreamrequest.ResourceSource{{API: response.API, Node: response.Node, SongID: response.SongID, ResourceURL: response.URL, ObservedAt: response.ObservedAt}}, 10*time.Minute)
	if err == nil {
		err = store.ObserveSongURL(ctx, response.SongURL())
	}
	if err != nil && ctx.Err() == nil {
		// A local persistence failure does not change the API's health result.
		slog.Warn("metadata_sync_url_sync_failed", "song_id", response.SongID, "node", response.Node, "error", applog.SafeError(err))
	}
	if err == nil && ctx.Err() == nil {
		c.catalogDataUpdated()
	}
}

func (c *Console) rejectPlaybackURL(ctx context.Context, observation cacheproxy.SongURL) {
	if ctx.Err() != nil {
		return
	}
	store, err := c.businessStore()
	if err == nil {
		err = store.RejectSongURL(ctx, observation)
	}
	if err != nil && ctx.Err() == nil {
		slog.Warn("metadata_sync_url_reject_failed", "song_id", observation.SongID, "error", applog.SafeError(err))
	}
}
