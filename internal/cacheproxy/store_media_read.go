package cacheproxy

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) songResource(ctx context.Context, id string) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", id).Scan(&key)
	return key, err
}
func (s *Store) resourceSongs(ctx context.Context, key, preferred string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sm.song_id FROM song_media sm
 LEFT JOIN catalog_members c ON c.song_id=sm.song_id
 WHERE sm.md5=?
 ORDER BY sm.song_id=? DESC,c.song_id IS NOT NULL DESC,sm.song_id DESC LIMIT ?`, key, preferred, limit)
	if err != nil {
		return nil, err
	}
	var result []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		result = append(result, id)
	}
	return result, errors.Join(err, rows.Err(), rows.Close())
}
func (s *Store) resourceSongLabels(ctx context.Context, key string, observed []string) ([]CacheSong, error) {
	ids := []byte("[]")
	if len(observed) > 0 {
		ids, _ = json.Marshal(observed)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ids.song_id, COALESCE(substr(s.name,1,300),'') FROM
 (SELECT song_id FROM song_media WHERE md5=? UNION SELECT CAST(value AS INTEGER) AS song_id FROM json_each(?)) ids
 LEFT JOIN songs s ON s.id=ids.song_id ORDER BY ids.song_id LIMIT 21`, key, string(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CacheSong{}
	for rows.Next() {
		var song CacheSong
		if err := rows.Scan(&song.ID, &song.Title); err != nil {
			return nil, err
		}
		result = append(result, song)
	}
	return result, rows.Err()
}

type resourceLabelLookup struct {
	Resource string `json:"resource"`
	SongID   string `json:"songID"`
}

// Labels are optional observation enrichment: a failed lookup leaves the
// caller's previous labels intact, without changing download decisions.
func (s *Store) resourceLabels(ctx context.Context, missing []resourceLabelLookup) map[string]blockVideo {
	if s == nil || s.usage == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, _ := json.Marshal(missing)
	rows, err := s.db.QueryContext(ctx, `
 WITH requested AS (
 SELECT json_extract(value,'$.resource') resource,
        json_extract(value,'$.songID') song_id FROM json_each(?)
 )
 SELECT r.resource, COALESCE(CAST(s.id AS TEXT),r.song_id,''),
        substr(COALESCE(s.name,''),1,300)
 FROM requested r LEFT JOIN songs s ON s.id=COALESCE(NULLIF(r.song_id,''),
 (SELECT sm.song_id FROM song_media sm WHERE sm.md5=r.resource ORDER BY sm.song_id LIMIT 1))`, string(data))
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := map[string]blockVideo{}
	for rows.Next() {
		var key string
		var v blockVideo
		if rows.Scan(&key, &v.SongID, &v.Title) == nil {
			result[key] = v
		}
	}
	return result
}
