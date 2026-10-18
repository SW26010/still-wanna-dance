package cacheproxy

import (
	"context"
	"time"
)

// Both admission and eviction rank the current catalog with the same policy.
func (s *Server) retentionPriorities(ctx context.Context) (map[string]ResourcePriority, error) {
	s.usage.flush()
	refs := make(map[string]string)
	rows, err := s.usage.db.QueryContext(ctx, "SELECT song_id,md5 FROM song_media")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return nil, err
		}
		refs[id] = key
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return s.resourcePriorities(ctx, refs, time.Now().UnixMilli())
}

func applyRetentionPriorities(videos []retainedVideo, priorities map[string]ResourcePriority) {
	for i := range videos {
		item := &videos[i]
		p := priorities[item.key]
		item.score, item.songID = p.Score, p.SongID
		if p.LastDemand != 0 {
			item.recent = p.LastDemand
		}
	}
}

func lowerRetentionPriority(a, b retainedVideo) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	if a.songID != b.songID {
		return a.songID < b.songID
	}
	if a.recent != b.recent {
		return a.recent < b.recent
	}
	if a.modified != b.modified {
		return a.modified < b.modified
	}
	return a.path < b.path
}
