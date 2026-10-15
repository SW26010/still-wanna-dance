package upstreamstate

import "context"

// Each pass visits incomplete songs in priority order. Failed resolutions or
// conflicting MD5s cannot monopolize every batch; retry them on the next pass.
// The ordinary catalog sample remains independent, so completing the backlog
// does not turn the last selected song into the permanent fallback sample.
func (m *Monitor) selectPlaybackSong(ctx context.Context, revision uint64, kind CheckKind, fallback int64) int64 {
	if kind == CheckCatalog || m.playbackSongCandidates == nil {
		return fallback
	}
	if ctx.Err() != nil || m.channel.Snapshot().Revision != revision {
		return 0
	}
	ids := m.playbackSongCandidates(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.closed || m.refreshChannelLocked().Revision != revision {
		return 0
	}
	// Rebuild from the current backlog to bound memory and forget completed songs.
	attempted := make(map[int64]bool, len(ids))
	var selected, first int64
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if first == 0 {
			first = id
		}
		if m.playbackSongsAttempted[id] {
			attempted[id] = true
		} else if selected == 0 {
			selected = id
		}
	}
	if selected == 0 && first != 0 {
		selected = first
		clear(attempted)
	}
	m.playbackSongsAttempted = attempted
	if selected == 0 {
		return fallback
	}
	m.playbackSongsAttempted[selected] = true
	return selected
}
