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
	rows, err := s.usage.db.QueryContext(ctx, `SELECT CAST(sv.song_id AS INTEGER), SUM(u.demand_count), MAX(u.last_demand_at)
		FROM song_videos sv JOIN resource_usage u ON u.resource_key = sv.version_key
		WHERE u.demand_count > 0 GROUP BY sv.song_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, count, last int64
		if err := rows.Scan(&id, &count, &last); err != nil {
			return nil, err
		}
		result[id] = retentionScore(count, last, now)
	}
	return result, rows.Err()
}
