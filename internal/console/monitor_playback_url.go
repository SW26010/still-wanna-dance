package console

import (
	"context"
	"log/slog"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

func (c *Console) monitorPlaybackSongCandidates(ctx context.Context) []int64 {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	engine := c.service
	closing := c.closing
	c.mu.Unlock()
	// Catalog delivery opens storage when appropriate. Sampling alone does not
	// initialize an empty library, or reopen a missing/unavailable one.
	if engine == nil || closing || ctx.Err() != nil {
		return nil
	}
	ids, err := engine.SongMetadataCandidates(ctx, c.apiBase+"/Api/Songs/play")
	if err != nil && ctx.Err() == nil {
		slog.Warn("monitor_song_metadata_candidates_failed", "error", applog.SafeError(err))
	}
	return ids
}

// Reuse the successful probe response without resolving or loading the song.
func (c *Console) syncMonitorPlaybackURL(ctx context.Context, response upstreamstate.PlaybackURLResponse) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.closing || ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	err := c.ensureEngine()
	engine := c.service
	c.mu.Unlock()
	if err == nil {
		err = engine.ObserveSongURL(ctx, cacheproxy.SongURL{
			SongID: response.SongID, URL: response.URL, API: response.API, Node: response.Node,
			QueryStartedAt: response.QueryStartedAt, ObservedAt: response.ObservedAt,
		})
	}
	if err != nil && ctx.Err() == nil {
		// A local persistence failure does not change the API's health result.
		slog.Warn("monitor_song_url_sync_failed", "song_id", response.SongID, "node", response.Node, "error", applog.SafeError(err))
	}
}
