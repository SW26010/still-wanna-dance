package cacheproxy

import (
	"context"
	"sort"
	"time"

	"still-wanna-dance/internal/playbacksource"
)

// SongMetadataCandidates ranks missing URL/size metadata by individual song
// scores. Songs without a mapping are candidates too.
// Historical URLs count as metadata: their playback freshness is independently
// checked by SongURLs. Expiry alone must not keep starving unfilled songs.
func (s *Store) SongMetadataCandidates(ctx context.Context, api string) (resultValue []int64, resultErr error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	s.usage.flush()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	filter, args := songURLNodeFilter(playbacksource.Nodes())
	args = append([]any{api}, args...)
	rows, err := s.usage.db.QueryContext(ctx, `SELECT s.id FROM songs s
 LEFT JOIN song_media sm ON sm.song_id=s.id LEFT JOIN media m ON m.md5=sm.md5
 WHERE m.byte_size IS NULL OR m.byte_size<=0
  OR NOT EXISTS(SELECT 1 FROM song_urls u WHERE u.song_id=s.id AND u.api=?`+filter+`
    AND u.md5=sm.md5 AND u.url<>'' AND u.failed_at=0)`, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
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
