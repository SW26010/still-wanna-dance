package cacheproxy

import (
	"context"
	"sort"
	"strconv"
	"time"

	initialpriority "still-wanna-dance/data/initial-priority"
)

// Real demand replaces the initial prior, and survives content changes.
func (s *Server) songPriorities(ctx context.Context, ids []int64, now int64) (map[int64]float64, error) {
	result := make(map[int64]float64, len(ids))
	for _, id := range ids {
		result[id] = initialpriority.Score(id)
	}
	rows, err := s.usage.db.QueryContext(ctx, `SELECT song_id, demand_score, last_demand_at
		FROM song_usage WHERE demand_count > 0`)
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

type ResourcePriority struct {
	Score      float64
	SongID     int64
	LastDemand int64
}

// EffectiveResourcePriorities is shared by batch ordering and eviction.
// References are a caller-owned current-mapping snapshot; each song is counted once.
func (s *Server) EffectiveResourcePriorities(ctx context.Context, refs map[string]string) (map[string]ResourcePriority, error) {
	s.usage.flush()
	return s.resourcePriorities(ctx, refs, time.Now().UnixMilli())
}
func (s *Server) resourcePriorities(ctx context.Context, refs map[string]string, now int64) (map[string]ResourcePriority, error) {
	ids := make([]int64, 0, len(refs))
	for id := range refs {
		n, _ := strconv.ParseInt(id, 10, 64)
		ids = append(ids, n)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	priorities, err := s.songPriorities(ctx, ids, now)
	if err != nil {
		return nil, err
	}
	rows, err := s.usage.db.QueryContext(ctx, "SELECT song_id,last_demand_at FROM song_usage")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recent := map[int64]int64{}
	for rows.Next() {
		var id, last int64
		if err := rows.Scan(&id, &last); err != nil {
			return nil, err
		}
		recent[id] = last
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]ResourcePriority{}
	for _, n := range ids {
		key := refs[strconv.FormatInt(n, 10)]
		p := result[key]
		p.Score += priorities[n]
		p.SongID = max(p.SongID, n)
		p.LastDemand = max(p.LastDemand, recent[n])
		result[key] = p
	}
	return result, nil
}
