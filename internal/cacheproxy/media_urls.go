package cacheproxy

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"

	"still-wanna-dance/internal/playbacksource"
)

// PlaybackObservation is a successful API result for an explicit query song.
// Request timestamps and the API endpoint are not resource versions.
type PlaybackObservation struct {
	SongID    int64
	URL, Node string
}

// MediaURL belongs to content, not the song that happened to resolve it.
// Size is read from media; it is not duplicated in the address table.
type MediaURL struct {
	URL, MD5, Node string
	Size           int64
}

func (s *Server) ObserveSongURL(ctx context.Context, o PlaybackObservation) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return errors.New("invalid playback URL")
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

// ObserveSongURL commits the address, resource properties and this song's
// current mapping together. Successful observations overwrite in commit order.
func (s *Store) ObserveSongURL(ctx context.Context, o PlaybackObservation) (resultErr error) {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	if o.SongID <= 0 || strings.TrimSpace(o.Node) == "" {
		return errors.New("invalid playback observation")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return errors.New("invalid playback URL")
	}
	v, err := parseVideoURL(req, math.MaxInt64-1)
	if err != nil {
		return err
	}
	defer func() { resultErr = s.metadataWriteError(ctx, resultErr) }()
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, upsertMediaSQL, v.key, v.size); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_urls(md5,node,url) VALUES (?,?,?)
 ON CONFLICT(md5,node) DO UPDATE SET url=excluded.url`, v.key, o.Node, o.URL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO songs(id) VALUES (?) ON CONFLICT DO NOTHING", o.SongID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO song_media(song_id,md5) VALUES (?,?)
 ON CONFLICT(song_id) DO UPDATE SET md5=excluded.md5`, o.SongID, v.key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(false)
	return nil
}

// SongURLs follows the current mapping; reading never changes that mapping.
// Empty node allows inspection of all nodes, independent of playback policy.
func (s *Store) SongURLs(ctx context.Context, id int64, node string) ([]MediaURL, error) {
	return s.mediaURLs(ctx, "song", id, observationNodes(node), -1, nil)
}

func (s *Store) ResourceURLs(ctx context.Context, key, node string, limit int) ([]MediaURL, error) {
	return s.mediaURLs(ctx, "media", key, observationNodes(node), limit, nil)
}

func (s *Store) AvailableMediaURLs(ctx context.Context) ([]MediaURL, error) {
	return s.mediaURLs(ctx, "", nil, playbacksource.Nodes(), -1, nil)
}

// FirstAvailableMediaURL reads one accepted address without loading the full list.
func (s *Store) FirstAvailableMediaURL(ctx context.Context, accept func(string) bool) (MediaURL, bool, error) {
	if accept == nil {
		accept = func(string) bool { return true }
	}
	urls, err := s.mediaURLs(ctx, "", nil, playbacksource.Nodes(), -1, accept)
	if err != nil || len(urls) == 0 {
		return MediaURL{}, false, err
	}
	return urls[0], true, nil
}

// acceptFirst optionally filters addresses and stops at the first accepted one.
func (s *Store) mediaURLs(ctx context.Context, scope string, value any, nodes []string, limit int, acceptFirst func(string) bool) (result []MediaURL, resultErr error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	filter, args := mediaURLNodeFilter(nodes)
	switch scope {
	case "song":
		filter += " AND u.md5=(SELECT md5 FROM song_media WHERE song_id=?)"
		args = append(args, value)
	case "media":
		filter += " AND u.md5=?"
		args = append(args, value)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT u.url,u.md5,u.node,COALESCE(m.byte_size,0)
 FROM media_urls u JOIN media m ON m.md5=u.md5 WHERE 1`+filter+` ORDER BY u.md5,u.node LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u MediaURL
		if err := rows.Scan(&u.URL, &u.MD5, &u.Node, &u.Size); err != nil {
			return nil, err
		}
		if validMediaURL(u) && (acceptFirst == nil || acceptFirst(u.URL)) {
			result = append(result, u)
			if acceptFirst != nil {
				break
			}
		}
	}
	return result, rows.Err()
}

func validMediaURL(u MediaURL) bool {
	r, err := http.NewRequest(http.MethodGet, u.URL, nil)
	if err != nil {
		return false
	}
	v, err := parseVideoURL(r, math.MaxInt64-1)
	return err == nil && v.key == u.MD5 && (u.Size == 0 || v.size == u.Size) && strings.TrimSpace(u.Node) != ""
}

// nil is unrestricted inspection. Source policy always filters before LIMIT.
func observationNodes(node string) []string {
	if node == "" {
		return nil
	}
	return []string{node}
}

func mediaURLNodeFilter(nodes []string) (string, []any) {
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
	return " AND u.node IN (" + strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + ")", args
}
