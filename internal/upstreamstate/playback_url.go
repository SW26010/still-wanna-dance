package upstreamstate

import (
	"context"
	"time"
)

// PlaybackURLResponse is transient API evidence, never included in status
// snapshots. API excludes query parameters; Node is the API value (cf/nya).
// Keep request start/completion times so delayed writes cannot renew old data.
type PlaybackURLResponse struct {
	SongID                     int64
	URL, API, Node             string
	QueryStartedAt, ObservedAt time.Time
}

func (m *Monitor) deliverPlaybackURL(ctx context.Context, revision uint64, response PlaybackURLResponse) {
	if m.onPlaybackURL == nil || response.URL == "" || ctx.Err() != nil || m.channel.Snapshot().Revision != revision {
		return
	}
	m.onPlaybackURL(ctx, response)
}
