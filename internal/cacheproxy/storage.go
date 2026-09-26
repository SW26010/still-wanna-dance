package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"stepstash/internal/applog"
)

func (c Config) videosDir() string { return filepath.Join(c.StorageDir, "videos") }
func (c Config) tempDir() string   { return filepath.Join(c.StorageDir, "tmp") }
func (c Config) videoFile(key string) string {
	return filepath.Join(c.videosDir(), key+".mp4")
}

type songConfirmation struct {
	gate chan struct{}
	refs int
}

// Only the map bookkeeping is global. Waiting for an API response or another
// confirmation is per song, cancellable, and does not retain idle lock entries.
func (s *Server) lockConfirmation(ctx context.Context, id string) (func(), error) {
	s.currentMu.Lock()
	if s.currentLocks == nil {
		s.currentLocks = make(map[string]*songConfirmation)
	}
	lock := s.currentLocks[id]
	if lock == nil {
		lock = &songConfirmation{gate: make(chan struct{}, 1)}
		s.currentLocks[id] = lock
	}
	lock.refs++
	s.currentMu.Unlock()
	drop := func() {
		s.currentMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.currentLocks, id)
		}
		s.currentMu.Unlock()
	}
	select {
	case lock.gate <- struct{}{}:
		return func() { <-lock.gate; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// recordVideo records a resource without inferring a song from its URL.
func (s *Server) recordVideo(ctx context.Context, v video) error {
	_, err := s.usage.db.ExecContext(ctx, `INSERT INTO video_versions(version_key, checksum, file_bytes, source_path)
 VALUES (?, ?, ?, ?) ON CONFLICT(version_key) DO NOTHING`, v.key, v.checksum, v.size, v.path)
	return err
}

// Each explicit caller records its own mapping after the shared flight completes.
func (s *Server) recordSongVideo(ctx context.Context, id string, v video) error {
	// Protect the resource through commit and cleanup-query invalidation,
	// including callers that do not already own a request/worker pin.
	s.pinVideo(v)
	defer s.releaseVideo(v)
	unlock, err := s.lockConfirmation(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	var current string
	err = s.usage.db.QueryRowContext(ctx, `SELECT version_key FROM current_videos WHERE song_id=?`, id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	promote := current == "" || current == v.key
	if !promote {
		latest, err := s.currentVideo(ctx, id)
		if err != nil {
			s.cfg.Logger.Warn("current_version_unavailable", "song_id", id, "error", err)
		} else {
			promote = latest.key == v.key
		}
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
	s.mappingRevision++
	if current != "" && current != v.key {
		s.cleanupNeeded[current] = true
	}
	s.cleanupNeeded[v.key] = true
	s.requestRetentionLocked()
	return nil
}

// Only the song API establishes which conflicting version is current. A normal
// playback URL, download completion time, and a hash provide no ordering.
func (s *Server) currentVideo(ctx context.Context, id string) (video, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var target string
	var err error
	if s.cfg.ResolveCurrent != nil {
		target, err = s.cfg.ResolveCurrent(ctx, id)
	} else {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.udon.dance/Api/Songs/play?node=nya&id="+id, nil)
		if reqErr != nil {
			return video{}, reqErr
		}
		resp, requestErr := s.client.Do(req)
		if requestErr != nil {
			return video{}, applog.SafeError(requestErr)
		}
		defer resp.Body.Close()
		if !IsSongRedirect(resp.StatusCode) {
			return video{}, fmt.Errorf("song API status %d", resp.StatusCode)
		}
		target = resp.Header.Get("Location")
	}
	if err != nil {
		return video{}, applog.SafeError(err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return video{}, applog.SafeError(err)
	}
	if (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.User != nil || r.URL.Fragment != "" {
		return video{}, errors.New("invalid current video URL")
	}
	return s.parse(r)
}

// Called by the serialized retention worker, including when the cache is unlimited.
func (s *Server) cleanSuperseded() {
	s.retentionMu.Lock()
	pending := s.cleanupNeeded
	s.cleanupNeeded = make(map[string]bool)
	s.retentionMu.Unlock()
	for key := range pending {
		if !s.cleanSupersededVideo(key) {
			s.retentionMu.Lock()
			s.cleanupNeeded[key] = true
			s.retentionMu.Unlock()
		}
	}
}

func (s *Server) cleanSupersededVideo(key string) bool {
	s.retentionMu.Lock()
	revision := s.mappingRevision
	pinned := s.versionPins[key] > 0
	s.retentionMu.Unlock()
	if pinned {
		return false // The last release wakes the worker again.
	}
	var obsolete bool
	err := s.usage.db.QueryRow(`SELECT
 EXISTS(SELECT 1 FROM song_videos WHERE version_key=?) AND
 NOT EXISTS(SELECT 1 FROM current_videos WHERE version_key=?)`, key, key).Scan(&obsolete)
	if err != nil {
		s.cfg.Logger.Warn("version_cleanup_failed", "key", key, "error", err)
		return false
	}
	s.retentionMu.Lock()
	// A mapping commit invalidates the query. recordSongVideo pins its
	// candidate until it has advanced this revision, closing the commit gap.
	if revision != s.mappingRevision {
		s.retentionMu.Unlock()
		return false
	}
	if !obsolete {
		s.retentionMu.Unlock()
		return true
	}
	reserved := s.beginVideoRemovalLocked(key)
	s.retentionMu.Unlock()
	if !reserved {
		return false
	}
	removed := false
	defer func() { s.finishVideoRemoval(key, removed) }()
	path := s.cfg.videoFile(key)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		removed = true
		return true
	}
	if err == nil {
		err = os.Remove(path)
	}
	if err != nil && !os.IsNotExist(err) {
		s.cfg.Logger.Warn("version_cleanup_failed", "key", key, "error", err)
		return false
	}
	removed = true
	s.cfg.Logger.Info("superseded_video_removed", "key", key, "path", path)
	return true
}

func (s *Server) cleanSupersededOnStartup() {
	rows, err := s.usage.db.Query(`SELECT DISTINCT version_key FROM song_videos
 WHERE version_key NOT IN (SELECT version_key FROM current_videos)`)
	if err != nil {
		s.cfg.Logger.Warn("version_cleanup_failed", "error", err)
		return
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			break
		}
		keys = append(keys, key)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		s.cfg.Logger.Warn("version_cleanup_failed", "error", err)
		return
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	for _, key := range keys {
		s.cleanupNeeded[key] = true
	}
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
