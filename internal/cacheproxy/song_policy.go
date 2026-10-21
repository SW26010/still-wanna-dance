package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
)

// SongPolicy is local user data, independent of catalog metadata and usage scores.
type SongPolicy string

const (
	SongNormal   SongPolicy = "normal"
	SongBlocked  SongPolicy = "blocked"
	SongFavorite SongPolicy = "favorite"
)

var ErrSongBlocked = errors.New("歌曲已屏蔽")

func (p SongPolicy) Valid() bool {
	return p == SongNormal || p == SongBlocked || p == SongFavorite
}

func readSongPolicy(ctx context.Context, db *sql.DB, id int64) (SongPolicy, error) {
	var policy SongPolicy
	err := db.QueryRowContext(ctx, "SELECT policy FROM song_preferences WHERE song_id=?", id).Scan(&policy)
	if errors.Is(err, sql.ErrNoRows) {
		return SongNormal, nil
	}
	return policy, err
}

// SongPolicies is a sparse snapshot for source selection and display. Entry
// points still check admission when creating work; this snapshot owns no demand.
func (s *Server) SongPolicies(ctx context.Context) (map[int64]SongPolicy, error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	rows, err := s.usage.db.QueryContext(ctx, "SELECT song_id,policy FROM song_preferences")
	if err != nil {
		return nil, s.storageError(err)
	}
	defer rows.Close()
	result := make(map[int64]SongPolicy)
	for rows.Next() {
		var id int64
		var policy SongPolicy
		if err := rows.Scan(&id, &policy); err != nil {
			return nil, err
		}
		result[id] = policy
	}
	return result, rows.Err()
}

// CheckSongAllowed runs at song entry points, before creating a usage demand.
// Existing demands are unaffected by later policy changes.
func (s *Server) CheckSongAllowed(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid song ID")
	}
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	policy, err := readSongPolicy(ctx, s.usage.db, id)
	if err != nil {
		return s.storageError(err)
	}
	if policy == SongBlocked {
		return ErrSongBlocked
	}
	return nil
}

// SetSongPolicy commits the preference before waking ordinary retention.
// Serializing with publication/eviction prevents a newly favored file from being
// deleted by a retention decision made before this call completed.
func (s *Server) SetSongPolicy(ctx context.Context, id int64, policy SongPolicy) error {
	if id <= 0 || !policy.Valid() {
		return errors.New("invalid song preference")
	}
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	if err := writeSongPolicy(ctx, s.usage.db, id, policy); err != nil {
		return s.storageError(err)
	}
	s.retentionMu.Lock()
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
	return nil
}

// SetSongPolicyOffline writes an idle library without starting retention. In
// particular, favoriting an existing video must precede startup budget cleanup.
func SetSongPolicyOffline(ctx context.Context, root string, id int64, policy SongPolicy) error {
	if id <= 0 || !policy.Valid() {
		return errors.New("invalid song preference")
	}
	db, close, err := openOfflineStorage(root)
	if err != nil {
		return err
	}
	defer close()
	return writeSongPolicy(ctx, db, id, policy)
}

func writeSongPolicy(ctx context.Context, db *sql.DB, id int64, policy SongPolicy) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if policy == SongNormal {
		_, err = tx.ExecContext(ctx, "DELETE FROM song_preferences WHERE song_id=?", id)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO song_preferences(song_id,policy) VALUES (?,?)
 ON CONFLICT(song_id) DO UPDATE SET policy=excluded.policy`, id, policy)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

type resourcePolicy struct {
	favorite bool
	blocked  bool
}

// Only resources with at least one marked song need a policy entry. All current
// associations participate in the reduction, including ordinary songs sharing
// the same resource. No associations means ordinary, never implicitly blocked.
func (s *Server) resourcePolicies(ctx context.Context) (map[string]resourcePolicy, error) {
	rows, err := s.usage.db.QueryContext(ctx, `SELECT m.md5,
 MAX(CASE WHEN p.policy='favorite' THEN 1 ELSE 0 END),
 MIN(CASE WHEN p.policy='blocked' THEN 1 ELSE 0 END)
 FROM song_media m LEFT JOIN song_preferences p ON p.song_id=m.song_id
 WHERE m.md5 IN (SELECT sm.md5 FROM song_media sm JOIN song_preferences sp ON sp.song_id=sm.song_id)
 GROUP BY m.md5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]resourcePolicy)
	for rows.Next() {
		var key string
		var p resourcePolicy
		if err := rows.Scan(&key, &p.favorite, &p.blocked); err != nil {
			return nil, err
		}
		result[key] = p
	}
	return result, rows.Err()
}
