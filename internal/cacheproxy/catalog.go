package cacheproxy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	names := make([]catalogName, 0, len(c.Songs))
	for _, song := range c.Songs {
		name := ""
		if song.Name != nil {
			name = *song.Name
		}
		names = append(names, catalogName{ID: song.ID, Name: name})
	}
	nameDigest := catalogNamesDigest(names)
	if _, err := checkCatalogState(ctx, tx, "song_names", c.Revision, nameDigest); err != nil {
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
	if err := writeCatalogState(ctx, tx, "song_names", c.Revision, nameDigest, c.Source); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.refreshCatalogProtection(c.Songs)
	return nil
}

type catalogName struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func catalogNamesDigest(names []catalogName) string {
	body, _ := json.Marshal(names) // Only int64 and string fields.
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

// SyncCatalogNames applies the existing Udon ID/name fallback atomically. Its
// common-field watermark also prevents it from overwriting a newer MD5 catalog.
// It neither changes mappings nor advances the full MD5-content watermark.
func (s *Server) SyncCatalogNames(ctx context.Context, revision, source string, names map[string]string) error {
	if len(names) == 0 || source == "" {
		return fmt.Errorf("清单为空或来源无效")
	}
	entries := make([]catalogName, 0, len(names))
	for id, name := range names {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
			return fmt.Errorf("%w: %s", ErrInvalidCatalogMapping, id)
		}
		entries = append(entries, catalogName{ID: n, Name: name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	digest := catalogNamesDigest(entries)
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
	duplicate, err := checkCatalogState(ctx, tx, "song_names", revision, digest)
	if err != nil || duplicate {
		return err
	}
	for _, song := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO songs(song_id,name) VALUES (?,?)
 ON CONFLICT(song_id) DO UPDATE SET name=excluded.name`, song.ID, song.Name); err != nil {
			return err
		}
	}
	if err := writeCatalogState(ctx, tx, "song_names", revision, digest, source); err != nil {
		return err
	}
	return tx.Commit()
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
