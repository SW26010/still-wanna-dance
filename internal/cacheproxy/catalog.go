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
// The caller retains its existing authoritative endpoint; source is provenance.
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
	var revision, previousDigest string
	err = tx.QueryRowContext(ctx, "SELECT revision,digest FROM catalog_state WHERE catalog_key='songs'").Scan(&revision, &previousDigest)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if c.Revision < revision {
			return fmt.Errorf("清单版本较旧，未更新")
		}
		if c.Revision == revision {
			if digest != previousDigest {
				return fmt.Errorf("清单同版本内容冲突，未更新")
			}
			return nil
		}
	}
	columns := strings.Split(songColumns, ",")
	assignments := make([]string, 0, len(columns)-1)
	for _, col := range columns[1:] {
		assignments = append(assignments, col+"=excluded."+col)
	}
	query := "INSERT INTO songs(" + songColumns + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") ON CONFLICT(song_id) DO UPDATE SET " + strings.Join(assignments, ",")
	for _, song := range c.Songs {
		_, err = tx.ExecContext(ctx, query, song.ID, song.Name, song.Artist, song.Dancer, song.PlayerCount, song.Volume, song.Start, song.End, song.Flip, song.DoubleWidth, song.SkipRandom, song.DisablePublic, song.RPE, song.Genre, song.Group, song.ComposedTitle, song.ComposedTitleSpell, song.AyaID, nullableJSON(song.Tags), nullableJSON(song.OriginalURLs), nullableJSON(song.ShaderMotion))
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
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_state(catalog_key,revision,digest,source,accepted_at) VALUES ('songs',?,?,?,?)
 ON CONFLICT(catalog_key) DO UPDATE SET revision=excluded.revision,digest=excluded.digest,source=excluded.source,accepted_at=excluded.accepted_at`, c.Revision, digest, c.Source, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.refreshCatalogProtection(c.Songs)
	return nil
}
