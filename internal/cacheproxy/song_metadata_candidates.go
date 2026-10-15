package cacheproxy

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// SongMetadataCandidates ranks missing URL/size metadata by the same effective
// resource score used for retention and batch preparation. All mapped songs
// contribute to shared-resource scores, including already complete songs.
// Historical URLs count as metadata: their playback freshness is independently
// checked by SongURLs. Expiry alone must not keep starving unfilled songs.
func (s *Server) SongMetadataCandidates(ctx context.Context, api string) ([]int64, error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	s.usage.flush()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	rows, err := s.usage.db.QueryContext(ctx, `SELECT sm.song_id,sm.md5,
 (m.byte_size IS NULL OR m.byte_size<=0
  OR NOT EXISTS(SELECT 1 FROM song_urls u WHERE u.song_id=sm.song_id AND u.api=? AND u.node='cf'
    AND u.md5=sm.md5 AND u.url<>'' AND u.failed_at=0)
  OR NOT EXISTS(SELECT 1 FROM song_urls u WHERE u.song_id=sm.song_id AND u.api=? AND u.node='nya'
    AND u.md5=sm.md5 AND u.url<>'' AND u.failed_at=0))
 FROM song_media sm JOIN media m ON m.md5=sm.md5`, api, api)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	var ids []int64
	for rows.Next() {
		var id int64
		var key string
		var missing bool
		if err := rows.Scan(&id, &key, &missing); err != nil {
			rows.Close()
			return nil, err
		}
		refs[strconv.FormatInt(id, 10)] = key
		if missing {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	priorities, err := s.resourcePriorities(ctx, refs, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := priorities[refs[strconv.FormatInt(ids[i], 10)]], priorities[refs[strconv.FormatInt(ids[j], 10)]]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.SongID != b.SongID {
			return a.SongID > b.SongID
		}
		return ids[i] > ids[j]
	})
	return ids, nil
}
