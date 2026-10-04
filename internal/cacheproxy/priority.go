package cacheproxy

import (
	"context"
	"time"

	initialpriority "still-wanna-dance/data/initial-priority"
)

// EffectiveSongPriorities replaces the prior, rather than adding to it, once
// a song has real demand. Its history survives content changes and eviction.
func (s *Server) EffectiveSongPriorities(ctx context.Context, ids []int64) (map[int64]float64, error) {
	s.usage.flush()
	return s.songPriorities(ctx, ids, time.Now().UnixMilli())
}

func (s *Server) songPriorities(ctx context.Context, ids []int64, now int64) (map[int64]float64, error) {
	result := make(map[int64]float64, len(ids))
	for _, id := range ids {
		result[id] = initialpriority.Score(id)
	}
	rows, err := s.usage.db.QueryContext(ctx, `SELECT CAST(substr(resource_key,6) AS INTEGER), demand_score, last_demand_at
		FROM resource_usage WHERE resource_key LIKE 'song:%' AND demand_count > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, last int64
		var score float64
		if err := rows.Scan(&id, &score, &last); err != nil {
			return nil, err
		}
		result[id] = retentionScore(score, last, now)
	}
	return result, rows.Err()
}
