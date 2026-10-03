package cacheproxy

import (
	"context"
	"time"

	initialpriority "still-wanna-dance/data/initial-priority"
)

// EffectiveSongPriorities replaces the prior, rather than adding to it, once
// a song has real demand. Historical versions retain that evidence after eviction.
func (s *Server) EffectiveSongPriorities(ctx context.Context, ids []int64) (map[int64]float64, error) {
	s.usage.flush()
	return s.songPriorities(ctx, ids, time.Now().UnixMilli())
}

func (s *Server) songPriorities(ctx context.Context, ids []int64, now int64) (map[int64]float64, error) {
	result := make(map[int64]float64, len(ids))
	for _, id := range ids {
		result[id] = initialpriority.Score(id)
	}
	rows, err := s.usage.db.QueryContext(ctx, `SELECT CAST(sv.song_id AS INTEGER), u.demand_score, u.last_demand_at
		FROM song_videos sv JOIN resource_usage u ON u.resource_key = sv.version_key
		WHERE u.demand_count > 0 ORDER BY sv.song_id, sv.version_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[int64]bool)
	for rows.Next() {
		var id, last int64
		var score float64
		if err := rows.Scan(&id, &score, &last); err != nil {
			return nil, err
		}
		if !seen[id] {
			result[id] = 0
			seen[id] = true
		}
		// Versions must be decayed to a common instant before adding them.
		result[id] += retentionScore(score, last, now)
	}
	return result, rows.Err()
}
