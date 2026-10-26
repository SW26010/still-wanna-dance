package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
)

func (c Config) videosDir() string  { return c.VideoDir }
func (c Config) tempDir() string    { return c.Work.Path() }
func (c Config) publishDir() string { return filepath.Join(c.VideoDir, ".swd-tmp") }
func (c Config) videoFile(key string) string {
	return filepath.Join(c.videosDir(), key+".mp4")
}

const upsertMediaSQL = `INSERT INTO media(md5, byte_size)
 VALUES (?, NULLIF(?,0)) ON CONFLICT(md5) DO UPDATE SET byte_size=COALESCE(excluded.byte_size,media.byte_size)`

// recordVideo records a resource without inferring a song from its URL.
func (s *Server) recordVideo(ctx context.Context, v video) error {
	_, err := s.usage.db.ExecContext(ctx, upsertMediaSQL, v.key, v.size)
	return s.storageError(err)
}

// Each explicit caller records its own mapping after the shared flight completes.
func (s *Server) recordSongVideo(ctx context.Context, id string, v video) (resultErr error) {
	defer func() { resultErr = s.storageError(resultErr) }()
	// Protect the resource through commit and cleanup-query invalidation,
	// including callers that do not already own a request/worker pin.
	s.pinVideo(v)
	defer s.releaseVideo(v)
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	var current string
	err := s.usage.db.QueryRowContext(ctx, `SELECT md5 FROM song_media WHERE song_id=?`, id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	promote := current == "" || current == v.key
	if !promote {
		s.cfg.Logger.Warn("song_md5_mismatch", "song_id", id, "mapped_md5", current, "url_md5", v.key)
	}
	if current != v.key {
		tx, err := s.usage.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
		if promote {
			if _, err = tx.ExecContext(ctx, `INSERT INTO song_media(song_id, md5) VALUES (?, ?)
 ON CONFLICT(song_id) DO NOTHING`, id, v.key); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	s.requestRetentionLocked()
	return nil
}
