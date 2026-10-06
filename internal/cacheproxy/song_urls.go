package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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

// ObserveSongURL preserves the complete URL and never changes catalog mappings.
// Each API/node has one latest observation, so signed URL churn is bounded.
func (s *Server) ObserveSongURL(ctx context.Context, o SongURL) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	api, err := url.Parse(o.API)
	if err != nil || api.Hostname() == "" || api.User != nil || api.Fragment != "" || (api.Scheme != "http" && api.Scheme != "https") || o.SongID <= 0 || o.QueryStartedAt.IsZero() || o.ObservedAt.Before(o.QueryStartedAt) {
		return errors.New("invalid song URL observation")
	}
	if err := ValidateVideoURL(o.URL, s.cfg.MaxFileBytes); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return err
	}
	v, err := s.parseResolved(req)
	if err != nil {
		return err
	}
	_, err = s.usage.db.ExecContext(ctx, `INSERT INTO song_urls
 (song_id,api,node,url,md5,byte_size,query_started_at,observed_at) VALUES (?,?,?,?,?,?,?,?)
 ON CONFLICT(song_id,api,node) DO UPDATE SET url=excluded.url, md5=excluded.md5,
 byte_size=excluded.byte_size, query_started_at=excluded.query_started_at,
 observed_at=excluded.observed_at, failed_at=0
 WHERE excluded.query_started_at>song_urls.query_started_at`,
		o.SongID, o.API, o.Node, o.URL, v.key, v.size, o.QueryStartedAt.UnixNano(), o.ObservedAt.UnixNano())
	if err != nil {
		return err
	}
	var mapped string
	if err := s.usage.db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", o.SongID).Scan(&mapped); err == nil && mapped != v.key {
		s.cfg.Logger.Warn("song_md5_mismatch", "song_id", o.SongID, "mapped_md5", mapped, "url_md5", v.key, "node", o.Node)
	}
	return nil
}

// SongURLs returns recent usable observations without probing them. Empty node
// accepts both API nodes; explicit node choices remain constraints.
func (s *Server) SongURLs(ctx context.Context, id int64, node string) ([]SongURL, error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	rows, err := s.usage.db.QueryContext(ctx, `SELECT song_id,url,md5,byte_size,api,node,query_started_at,observed_at
 FROM song_urls WHERE song_id=? AND failed_at=0 AND (?='' OR node=?) AND observed_at>=?
 ORDER BY query_started_at DESC`, id, node, node, time.Now().Add(-24*time.Hour).UnixNano())
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
		o.QueryStartedAt, o.ObservedAt = time.Unix(0, started), time.Unix(0, observed)
		if err := ValidateVideoURL(o.URL, s.cfg.MaxFileBytes); err == nil {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

// RejectSongURL only invalidates the observation actually attempted. A delayed
// failure cannot discard a subsequent successful API observation of the same URL.
func (s *Server) RejectSongURL(ctx context.Context, o SongURL) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	_, err := s.usage.db.ExecContext(ctx, `UPDATE song_urls SET failed_at=? WHERE song_id=? AND api=? AND node=? AND url=? AND query_started_at=?`,
		time.Now().UnixNano(), o.SongID, o.API, o.Node, o.URL, o.QueryStartedAt.UnixNano())
	return err
}
