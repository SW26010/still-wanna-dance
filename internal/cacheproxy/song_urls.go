package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SongURL records API evidence, not verification of the downloaded media.
// QueryStartedAt orders overlapping queries; ObservedAt describes completion.
type SongURL struct {
	SongID                     int64
	URL, MD5, API, Node        string
	Size                       int64
	QueryStartedAt, ObservedAt time.Time
}

// The engine additionally enforces its download limit and remembers discovered
// hosts. These admission rules are separate from the database observation.
func (s *Server) ObserveSongURL(ctx context.Context, o SongURL) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return err
	}
	if _, err := parseVideoURL(req, s.cfg.MaxFileBytes); err != nil {
		return err
	}
	if err := s.Store.ObserveSongURL(ctx, o); err != nil {
		return err
	}
	_, err = s.parseResolved(req)
	return err
}

// ObserveSongURL atomically records the URL, resource metadata and a missing
// song mapping, without downloading media or overwriting existing mappings.
// Each API/node has one latest observation, so signed URL churn is bounded.
func (s *Store) ObserveSongURL(ctx context.Context, o SongURL) (resultErr error) {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	// Persisted ordering uses wall time, not the process-local monotonic clock.
	o.QueryStartedAt, o.ObservedAt = o.QueryStartedAt.UTC(), o.ObservedAt.UTC()
	api, err := url.Parse(o.API)
	if err != nil || api.Hostname() == "" || api.User != nil || api.Fragment != "" || (api.Scheme != "http" && api.Scheme != "https") || o.SongID <= 0 || o.QueryStartedAt.IsZero() || o.ObservedAt.Before(o.QueryStartedAt) || !nanoTimeRepresentable(o.QueryStartedAt) || !nanoTimeRepresentable(o.ObservedAt) {
		return errors.New("invalid song URL observation")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return err
	}
	v, err := parseVideoURL(req, math.MaxInt64-1)
	if err != nil {
		return err
	}
	// Roll back/release the database transaction before retaining a storage fault.
	defer func() { resultErr = s.songURLStorageError(ctx, resultErr) }()
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	tx, err := s.usage.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Deferred foreign keys validate the observation with its song/resource at
	// commit, while stale observations still exit without changing either one.
	result, err := tx.ExecContext(ctx, `INSERT INTO song_urls
 (song_id,api,node,url,md5,byte_size,query_started_at,observed_at) VALUES (?,?,?,?,?,?,?,?)
 ON CONFLICT(song_id,api,node) DO UPDATE SET url=excluded.url, md5=excluded.md5,
 byte_size=excluded.byte_size, query_started_at=excluded.query_started_at,
 observed_at=excluded.observed_at, failed_at=0
 WHERE excluded.query_started_at>song_urls.query_started_at`,
		o.SongID, o.API, o.Node, o.URL, v.key, v.size, o.QueryStartedAt.UnixNano(), o.ObservedAt.UnixNano())
	if err != nil {
		return err
	}
	accepted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if accepted == 0 {
		return nil // A stale/tied observation must not update metadata or mappings either.
	}
	if _, err := tx.ExecContext(ctx, upsertMediaSQL, v.key, v.size); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", o.SongID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO song_media(song_id,md5) VALUES (?,?) ON CONFLICT(song_id) DO NOTHING", o.SongID, v.key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(false)
	var mapped string
	if err := s.usage.db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", o.SongID).Scan(&mapped); err == nil && mapped != v.key {
		s.log.Warn("song_md5_mismatch", "song_id", o.SongID, "mapped_md5", mapped, "url_md5", v.key, "node", o.Node)
	}
	return nil
}

func (s *Store) songURLStorageError(ctx context.Context, err error) error {
	// Cancellation may roll back the transaction before Commit checks its state.
	if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
		err = ctx.Err()
	}
	// Input validation has already succeeded before this boundary. Preserve the
	// existing terminal failure contract for an unexpected transaction failure,
	// including a constraint/trigger failure that invalidates this write.
	if !isLocalStorageError(err) {
		return err
	}
	if !errors.Is(err, ErrLocalStorage) {
		err = fmt.Errorf("%w: %w", ErrLocalStorage, err)
	}
	s.retainFailure(err)
	return err
}

// SongURLs returns recent usable observations without probing them. Empty node
// accepts all recorded nodes; this observation API does not apply source policy.
func (s *Store) SongURLs(ctx context.Context, id int64, node string) ([]SongURL, error) {
	return s.songURLs(ctx, "song_id", id, observationNodes(node), -1)
}

// ResourceURLs applies the same validity rules to a fixed resource. The caller
// chooses the candidate budget independently of any API lookup budget.
func (s *Store) ResourceURLs(ctx context.Context, key, node string, limit int) ([]SongURL, error) {
	return s.songURLs(ctx, "md5", key, observationNodes(node), limit)
}

// column is an internal SQL identifier, never caller-supplied input.
func (s *Store) songURLs(ctx context.Context, column string, value any, nodes []string, limit int) (resultValue []SongURL, resultErr error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	now := time.Now()
	filter, args := songURLNodeFilter(nodes)
	args = append([]any{value}, args...)
	args = append(args, now.Add(-24*time.Hour).UnixNano(), now.UnixNano(), limit)
	rows, err := s.usage.db.QueryContext(ctx, `SELECT song_id,url,md5,byte_size,api,node,query_started_at,observed_at
 FROM song_urls WHERE `+column+`=? AND failed_at=0`+filter+` AND observed_at>=? AND observed_at<=?
 ORDER BY query_started_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SongURL
	for rows.Next() {
		var o SongURL
		var started, observed int64
		if err := rows.Scan(&o.SongID, &o.URL, &o.MD5, &o.Size, &o.API, &o.Node, &started, &observed); err != nil {
			return nil, err
		}
		o.QueryStartedAt, o.ObservedAt = time.Unix(0, started).UTC(), time.Unix(0, observed).UTC()
		if err := ValidateVideoURL(o.URL, math.MaxInt64-1); err == nil {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

func nanoTimeRepresentable(t time.Time) bool {
	return time.Unix(0, t.UnixNano()).Equal(t)
}

// RejectSongURL only invalidates the observation actually attempted. A delayed
// failure cannot discard a subsequent successful API observation of the same URL.
func (s *Store) RejectSongURL(ctx context.Context, o SongURL) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	_, err := s.usage.db.ExecContext(ctx, `UPDATE song_urls SET failed_at=? WHERE song_id=? AND api=? AND node=? AND url=? AND query_started_at=?`,
		time.Now().UnixNano(), o.SongID, o.API, o.Node, o.URL, o.QueryStartedAt.UnixNano())
	return s.storageError(err)
}

// nil means unrestricted observation inspection; an empty eligibility list
// deliberately matches nothing. Filtering precedes ordering and LIMIT.
func observationNodes(node string) []string {
	if node == "" {
		return nil
	}
	return []string{node}
}
func songURLNodeFilter(nodes []string) (string, []any) {
	if nodes == nil {
		return "", nil
	}
	if len(nodes) == 0 {
		return " AND 0", nil
	}
	args := make([]any, len(nodes))
	for i, node := range nodes {
		args[i] = node
	}
	return " AND node IN (" + strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + ")", args
}
