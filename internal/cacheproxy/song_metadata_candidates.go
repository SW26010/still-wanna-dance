package cacheproxy

import (
	"context"
	"sort"
	"time"

	"still-wanna-dance/internal/playbacksource"
)

// SongMetadataCandidates ranks missing URL/size metadata by individual song
// scores. Songs without a mapping are candidates too.
// One resource address completes the chain for all currently associated songs.
func (s *Store) SongMetadataCandidates(ctx context.Context) (resultValue []int64, resultErr error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	s.usage.flush()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	filter, args := mediaURLNodeFilter(playbacksource.Nodes())
	rows, err := s.usage.db.QueryContext(ctx, `SELECT s.id,COALESCE(sm.md5,''),COALESCE(m.byte_size,0),COALESCE(u.node,''),COALESCE(u.url,'') FROM songs s
 LEFT JOIN song_media sm ON sm.song_id=s.id LEFT JOIN media m ON m.md5=sm.md5
 LEFT JOIN media_urls u ON u.md5=sm.md5`+filter, args...)
	if err != nil {
		return nil, err
	}
	complete := map[int64]bool{}
	for rows.Next() {
		var id int64
		var u MediaURL
		if err := rows.Scan(&id, &u.MD5, &u.Size, &u.Node, &u.URL); err != nil {
			rows.Close()
			return nil, err
		}
		complete[id] = complete[id] || u.Size > 0 && validMediaURL(u)
	}
	var ids []int64
	for id, ready := range complete {
		if !ready {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	priorities, err := s.songPriorities(ctx, ids, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := priorities[ids[i]], priorities[ids[j]]
		if a != b {
			return a > b
		}
		return ids[i] > ids[j]
	})
	return ids, nil
}
