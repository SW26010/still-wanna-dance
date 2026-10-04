package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
)

func (c Config) videosDir() string { return filepath.Join(c.StorageDir, "videos") }
func (c Config) tempDir() string   { return filepath.Join(c.StorageDir, "tmp") }
func (c Config) videoFile(key string) string {
	return filepath.Join(c.videosDir(), key+".mp4")
}

// recordVideo records a resource without inferring a song from its URL.
func (s *Server) recordVideo(ctx context.Context, v video) error {
	_, err := s.usage.db.ExecContext(ctx, `INSERT INTO video_versions(version_key, checksum, file_bytes, source_path)
 VALUES (?, ?, ?, ?) ON CONFLICT(version_key) DO UPDATE SET file_bytes=CASE WHEN excluded.file_bytes>0 THEN excluded.file_bytes ELSE video_versions.file_bytes END, source_path=CASE WHEN excluded.source_path<>'' THEN excluded.source_path ELSE video_versions.source_path END`, v.key, v.checksum, v.size, v.path)
	return err
}

// Each explicit caller records its own mapping after the shared flight completes.
func (s *Server) recordSongVideo(ctx context.Context, id string, v video) error {
	// Protect the resource through commit and cleanup-query invalidation,
	// including callers that do not already own a request/worker pin.
	s.pinVideo(v)
	defer s.releaseVideo(v)
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	var current string
	err := s.usage.db.QueryRowContext(ctx, `SELECT version_key FROM current_videos WHERE song_id=?`, id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	promote := current == "" || current == v.key
	if !promote {
		s.cfg.Logger.Warn("song_md5_mismatch", "song_id", id, "mapped_md5", current, "url_md5", v.key)
	}
	tx, err := s.usage.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO song_videos(song_id, version_key) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, v.key); err != nil {
		return err
	}
	if promote {
		if _, err = tx.ExecContext(ctx, `INSERT INTO current_videos(song_id, version_key) VALUES (?, ?)
 ON CONFLICT(song_id) DO UPDATE SET version_key=excluded.version_key`, id, v.key); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	s.rememberSongResourceLocked(id, v.key)
	s.requestRetentionLocked()
	return nil
}

// SetSongTitle updates catalog metadata independently of video versions.
func (s *Server) SetSongTitle(ctx context.Context, id, title string) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	_, err := s.usage.db.ExecContext(ctx, `INSERT INTO songs(song_id, title) VALUES (?, ?)
 ON CONFLICT(song_id) DO UPDATE SET title=excluded.title`, id, title)
	return err
}
