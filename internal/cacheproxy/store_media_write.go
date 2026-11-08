package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
)

func (s *Store) recordMediaSize(ctx context.Context, key string, size int64) error {
	_, err := s.db.ExecContext(ctx, upsertMediaSQL, key, size)
	return err
}

func (s *Store) recordSongMapping(ctx context.Context, id, key string, log *slog.Logger) error {
	var promote bool
	return s.writeMapping(ctx, func() (bool, error) {
		var current string
		err := s.db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", id).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		promote = current == "" || current == key
		if !promote {
			log.Warn("song_md5_mismatch", "song_id", id, "mapped_md5", current, "url_md5", key)
		}
		return current != key, nil
	}, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO songs(id) VALUES (?) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
		if promote {
			_, err := tx.ExecContext(ctx, `INSERT INTO song_media(song_id, md5) VALUES (?, ?)
 ON CONFLICT(song_id) DO NOTHING`, id, key)
			return err
		}
		return nil
	})
}
