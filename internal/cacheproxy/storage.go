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
)

func (c Config) videosDir() string { return filepath.Join(c.StorageDir, "videos") }
func (c Config) tempDir() string   { return filepath.Join(c.StorageDir, "tmp") }
func (c Config) videoFile(id, key string) string {
	return filepath.Join(c.videosDir(), id+"-"+key+".mp4")
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

// Records describe versions even after their bytes have been evicted. File
// presence and integrity are checked separately; no cached-present flag can drift.
func (s *Server) recordVideo(ctx context.Context, v video) error {
	// Serialize each song's authoritative observations with promotion, so a
	// slower old response cannot commit after a newer observation for that song.
	unlock, err := s.lockConfirmation(ctx, v.id)
	if err != nil {
		return err
	}
	defer unlock()
	var current string
	err = s.usage.db.QueryRowContext(ctx, `SELECT version_key FROM current_videos WHERE song_id=?`, v.id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	promote := current == "" || current == v.key
	if !promote {
		latest, err := s.currentVideo(ctx, v.id)
		if err != nil {
			s.cfg.Logger.Warn("current_version_unavailable", "song_id", v.id, "error", err)
		} else {
			promote = latest.key == v.key
		}
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	tx, err := s.usage.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", v.id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO video_versions(version_key, song_id, checksum, file_bytes, source_path)
 VALUES (?, ?, ?, ?, ?) ON CONFLICT(version_key) DO NOTHING`, v.key, v.id, v.checksum, v.size, v.path)
	if err != nil {
		return err
	}
	if promote {
		if _, err = tx.ExecContext(ctx, `INSERT INTO current_videos(song_id, version_key) VALUES (?, ?)
 ON CONFLICT(song_id) DO UPDATE SET version_key=excluded.version_key`, v.id, v.key); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if current != v.key {
		s.cleanupNeeded[v.id] = true
	}
	s.cleanSupersededLocked(v.id)
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
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://api.udon.dance/Api/Songs/play?node=nya&id="+id, nil)
		if reqErr != nil {
			return video{}, reqErr
		}
		resp, requestErr := s.client.Do(req)
		if requestErr != nil {
			return video{}, requestErr
		}
		defer resp.Body.Close()
		if resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 307 {
			return video{}, fmt.Errorf("song API status %d", resp.StatusCode)
		}
		target = resp.Header.Get("Location")
	}
	if err != nil {
		return video{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return video{}, err
	}
	if r.URL.Scheme != "http" || r.URL.User != nil || r.URL.Fragment != "" {
		return video{}, errors.New("invalid current video URL")
	}
	v, err := s.parse(r)
	if err == nil && v.id != id {
		err = errors.New("current video song ID mismatch")
	}
	return v, err
}

// Called under retentionMu, shared with pin acquisition and publication.
func (s *Server) cleanSupersededLocked(id string) {
	if !s.cleanupNeeded[id] {
		return
	}
	var key string
	if err := s.usage.db.QueryRow(`SELECT version_key FROM current_videos WHERE song_id=?`, id).Scan(&key); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.cfg.Logger.Warn("version_cleanup_failed", "song_id", id, "error", err)
		}
		return
	}
	entries, err := filepath.Glob(filepath.Join(s.cfg.videosDir(), id+"-*.mp4"))
	if err != nil {
		return
	}
	pending := false
	for _, path := range entries {
		match := cacheVideoName.FindStringSubmatch(filepath.Base(path))
		if match == nil || match[1] != id || match[2] == key {
			continue
		}
		if !s.removeSupersededLocked(path, id, match[2]) {
			pending = true
		}
	}
	if !pending {
		delete(s.cleanupNeeded, id)
	}
}

func (s *Server) removeSupersededLocked(path, id, key string) bool {
	if s.versionPins[key] > 0 {
		return false
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true
	}
	if err == nil && !info.Mode().IsRegular() {
		return true
	}
	if err == nil {
		err = os.Remove(path)
	}
	if err != nil {
		s.cfg.Logger.Warn("version_cleanup_failed", "song_id", id, "error", err)
		return false
	}
	s.cfg.Logger.Info("superseded_video_removed", "song_id", id, "path", path)
	return true
}

func (s *Server) cleanSupersededOnStartup() {
	rows, err := s.usage.db.Query(`SELECT song_id, version_key FROM current_videos`)
	if err != nil {
		s.cfg.Logger.Warn("version_cleanup_failed", "error", err)
		return
	}
	current := make(map[string]string)
	for rows.Next() {
		var id, key string
		if err = rows.Scan(&id, &key); err != nil {
			break
		}
		current[id] = key
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
	entries, err := os.ReadDir(s.cfg.videosDir())
	if err != nil {
		s.cfg.Logger.Warn("version_cleanup_failed", "error", err)
		return
	}
	for _, entry := range entries {
		match := cacheVideoName.FindStringSubmatch(entry.Name())
		if match == nil || current[match[1]] == "" || current[match[1]] == match[2] {
			continue
		}
		if !s.removeSupersededLocked(filepath.Join(s.cfg.videosDir(), entry.Name()), match[1], match[2]) {
			s.cleanupNeeded[match[1]] = true
		}
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
