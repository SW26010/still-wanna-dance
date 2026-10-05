package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const songColumns = "song_id,name,artist,dancer,player_count,volume,start,end,flip,double_width,skip_random,disable_public,rpe,genre,group_name,composed_title,composed_title_spell,aya_id,tags_json,original_urls_json,shader_motion_json"

func nullableJSON(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}

// SyncCatalog serializes version comparison and the complete catalog commit.
// Both MD5 endpoints share the songs watermark; source is provenance.
func (s *Server) SyncCatalog(ctx context.Context, c Catalog) error {
	c, digest, err := normalizeCatalog(c)
	if err != nil {
		return err
	}
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	tx, err := s.usage.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	duplicate, err := checkCatalogState(ctx, tx, "songs", c.Revision, digest)
	if err != nil {
		return err
	}
	if duplicate {
		return nil
	}
	columns := strings.Split(songColumns, ",")
	assignments := make([]string, 0, len(columns)-1)
	for _, col := range columns[1:] {
		assignments = append(assignments, col+"=excluded."+col)
	}
	query := "INSERT INTO songs(" + songColumns + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") ON CONFLICT(song_id) DO UPDATE SET " + strings.Join(assignments, ",")
	for _, song := range c.Songs {
		_, err = tx.ExecContext(ctx, query, song.ID, song.Name, song.Artist, song.Dancer, song.PlayerCount, song.Volume, song.Start, song.End, song.Flip, song.DoubleWidth, song.SkipRandom, song.DisablePublic, song.RPE, song.Genre, song.Group, song.ComposedTitle, song.ComposedTitleSpell, nullableJSON(song.AyaID), nullableJSON(song.Tags), nullableJSON(song.OriginalURLs), nullableJSON(song.ShaderMotion))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO media(md5) VALUES (?) ON CONFLICT DO NOTHING", song.MD5); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO song_media(song_id,md5) VALUES (?,?) ON CONFLICT(song_id) DO UPDATE SET md5=excluded.md5", song.ID, song.MD5); err != nil {
			return err
		}
	}
	if err := writeCatalogState(ctx, tx, "songs", c.Revision, digest, c.Source); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.refreshCatalogProtection(c.Songs)
	return nil
}

func checkCatalogState(ctx context.Context, tx *sql.Tx, key, revision, digest string) (bool, error) {
	candidateTime, err := ParseCatalogTime(revision)
	if err != nil {
		return false, err
	}
	var previousRevision, previousDigest string
	err = tx.QueryRowContext(ctx, "SELECT revision,digest FROM catalog_state WHERE catalog_key=?", key).Scan(&previousRevision, &previousDigest)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	previousTime, err := ParseCatalogTime(previousRevision)
	if err != nil {
		return false, err
	}
	if candidateTime.Before(previousTime) {
		return false, fmt.Errorf("清单时间较旧，未更新")
	}
	if candidateTime.Equal(previousTime) {
		if digest != previousDigest {
			return false, fmt.Errorf("清单同时间内容冲突，未更新")
		}
		return true, nil
	}
	return false, nil
}

func writeCatalogState(ctx context.Context, tx *sql.Tx, key, revision, digest, source string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO catalog_state(catalog_key,revision,digest,source,accepted_at) VALUES (?,?,?,?,?)
 ON CONFLICT(catalog_key) DO UPDATE SET revision=excluded.revision,digest=excluded.digest,source=excluded.source,accepted_at=excluded.accepted_at`, key, revision, digest, source, time.Now().UnixMilli())
	return err
}
