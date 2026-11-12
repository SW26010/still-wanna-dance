package cacheproxy

import (
	"context"
	"path/filepath"
)

func (c Config) videosDir() string  { return c.VideoDir }
func (c Config) tempDir() string    { return c.Work.Path() }
func (c Config) publishDir() string { return filepath.Join(c.VideoDir, ".swd-tmp") }
func (c Config) videoFile(key string) string {
	return filepath.Join(c.videosDir(), key+".mp4")
}

const upsertMediaSQL = `INSERT INTO media(md5, byte_size)
 VALUES (?, NULLIF(?,0)) ON CONFLICT(md5) DO UPDATE SET byte_size=excluded.byte_size
 WHERE excluded.byte_size IS NOT NULL AND excluded.byte_size IS NOT media.byte_size`

// recordVideo records a resource without inferring a song from its URL.
func (s *Server) recordVideo(ctx context.Context, v video) error {
	err := s.Store.recordMediaSize(ctx, v.key, v.size)
	return s.storageError(err)
}

// Each explicit caller records its own mapping after the shared flight completes.
func (s *Server) recordSongVideo(ctx context.Context, id string, v video) error {
	s.pinVideo(v)
	defer s.releaseVideo(v)
	return s.storageError(s.Store.recordSongMapping(ctx, id, v.key, s.cfg.Logger))
}
