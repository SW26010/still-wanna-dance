package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	mediaseed "still-wanna-dance/data/media-seed"
)

// initializeMediaSeed belongs to the creation transaction only.
func initializeMediaSeed(tx *sql.Tx, encoded string) error {
	ctx := context.Background()
	var seed mediaseed.Snapshot
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&seed); err != nil {
		return err
	}
	if _, err := ParseCatalogTime(seed.Version); err != nil {
		return err
	}
	if len(seed.Media) == 0 {
		return fmt.Errorf("预置资源为空")
	}
	media := make(map[string]bool, len(seed.Media))
	for _, resource := range seed.Media {
		key := resource.MD5
		if !validMD5(key) || media[key] || (resource.ByteSize != nil && *resource.ByteSize <= 0) {
			return fmt.Errorf("预置资源 MD5 无效或重复")
		}
		media[key] = true
	}
	ids := make(map[int64]bool, len(seed.SongMedia))
	for _, song := range seed.SongMedia {
		if song.ID <= 0 || ids[song.ID] || !media[song.MD5] {
			return fmt.Errorf("预置歌曲映射无效、重复或缺少资源")
		}
		ids[song.ID] = true
	}
	mediaStmt, err := tx.PrepareContext(ctx, "INSERT INTO media(md5,byte_size) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer mediaStmt.Close()
	for _, resource := range seed.Media {
		if _, err := mediaStmt.ExecContext(ctx, resource.MD5, resource.ByteSize); err != nil {
			return err
		}
	}
	songStmt, err := tx.PrepareContext(ctx, "INSERT INTO songs(id) VALUES (?)")
	if err != nil {
		return err
	}
	defer songStmt.Close()
	mappingStmt, err := tx.PrepareContext(ctx, "INSERT INTO song_media(song_id,md5) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer mappingStmt.Close()
	for _, song := range seed.SongMedia {
		if _, err := songStmt.ExecContext(ctx, song.ID); err != nil {
			return err
		}
		if _, err := mappingStmt.ExecContext(ctx, song.ID, song.MD5); err != nil {
			return err
		}
	}
	return nil
}

// KnownMedia returns immutable resource identities for an import operation.
// No song mapping, current membership, file or catalog watermark is required.
func (s *Store) KnownMedia(ctx context.Context) (keys map[string]bool, resultErr error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	rows, err := s.db.QueryContext(ctx, "SELECT md5 FROM media")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys = make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, rows.Err()
}
