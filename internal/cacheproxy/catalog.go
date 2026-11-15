package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"still-wanna-dance/internal/catalogmeta"
)

const songColumns = "id,name,artist,dancer,playerCount,volume,start,end,flip,double_width,skip_random,disable_public,rpe,major,groupName,tag,shader_motion"

const UdonCatalogURL = catalogmeta.UdonURL
const KivaCatalogURL = catalogmeta.KivaURL
const WannaCatalogURL = catalogmeta.WannaURL

// Only HTTPS Udon takes precedence over same-version backup metadata.
func IsPrimaryCatalog(source string) bool {
	return source == UdonCatalogURL
}

// CatalogPrecedes compares validated revisions; equal-priority ties are stable.
func CatalogPrecedes(a, b Catalog) bool {
	if a.Revision != b.Revision {
		return a.Revision > b.Revision
	}
	return IsPrimaryCatalog(a.Source) && !IsPrimaryCatalog(b.Source)
}

func nullableJSON(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}

// SyncCatalog serializes version comparison and the complete catalog commit.
// Metadata acceptance and backup resource fill are separate decisions in one
// transaction. Catalog omissions never delete accumulated business records.
func (s *Store) SyncCatalog(ctx context.Context, c Catalog) (resultErr error) {
	c, err := normalizeCatalog(c)
	if err != nil {
		return err
	}
	// Bind the resource candidates once, outside the mapping/retention locks.
	// Keep this payload separate from song metadata and its acceptance decision.
	type resource struct {
		ID  int64  `json:"id"`
		MD5 string `json:"md5"`
	}
	var resources []resource
	for _, song := range c.Songs {
		if song.MD5 != "" {
			if resources == nil {
				resources = make([]resource, 0, len(c.Songs))
			}
			resources = append(resources, resource{song.ID, song.MD5})
		}
	}
	var resourceJSON string
	if len(resources) != 0 {
		encoded, err := json.Marshal(resources)
		if err != nil {
			return err
		}
		resourceJSON = string(encoded)
	}
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	defer func() {
		if !errors.Is(resultErr, ErrCatalogOlder) {
			resultErr = s.metadataWriteError(ctx, resultErr)
		}
	}()
	var decisionErr error
	err = s.writeMapping(ctx, func() (bool, error) {
		s.observeCatalogVersion(c)
		return true, nil
	}, func(tx *sql.Tx) error {
		duplicate, err := checkSongCatalogState(ctx, tx, c)
		decisionErr = err
		if err != nil && !errors.Is(err, ErrCatalogOlder) {
			return err
		}
		// Even an older valid backup can fill holes, without making its
		// placeholders current members or changing already known mappings.
		if err := fillCatalogResources(ctx, tx, resourceJSON); err != nil {
			return err
		}
		if errors.Is(decisionErr, ErrCatalogOlder) || duplicate {
			return nil
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_members"); err != nil {
			return err
		}
		columns := strings.Split(songColumns, ",")
		assignments := make([]string, 0, len(columns)-1)
		for _, col := range columns[1:] {
			assignments = append(assignments, col+"=excluded."+col)
		}
		query := "INSERT INTO songs(" + songColumns + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") ON CONFLICT(id) DO UPDATE SET " + strings.Join(assignments, ",")
		for _, song := range c.Songs {
			_, err = tx.ExecContext(ctx, query, song.ID, song.Name, song.Artist, song.Dancer, song.PlayerCount, song.Volume, song.Start, song.End, song.Flip, song.DoubleWidth, song.SkipRandom, song.DisablePublic, song.RPE, song.Major, song.GroupName, nullableJSON(song.Tags), nullableJSON(song.ShaderMotion))
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_members(song_id) VALUES (?)", song.ID); err != nil {
				return err
			}
		}
		return writeCatalogState(ctx, tx, c.Revision, c.Source)
	})
	if err != nil {
		return err
	}
	return decisionErr
}

func fillCatalogResources(ctx context.Context, tx *sql.Tx, resources string) error {
	if resources == "" {
		return nil
	}
	// Filter before INSERT, rather than relying on no-op conflicts for every
	// song. DISTINCT also avoids repeated inserts for shared missing media.
	for _, query := range []string{
		`INSERT INTO songs(id)
 SELECT json_extract(c.value,'$.id') FROM json_each(?) c
 WHERE NOT EXISTS (SELECT 1 FROM songs WHERE id=json_extract(c.value,'$.id'))`,
		`INSERT INTO media(md5)
 SELECT DISTINCT json_extract(c.value,'$.md5') FROM json_each(?) c
 WHERE NOT EXISTS (SELECT 1 FROM media WHERE md5=json_extract(c.value,'$.md5'))`,
		`INSERT INTO song_media(song_id,md5)
 SELECT json_extract(c.value,'$.id'),json_extract(c.value,'$.md5') FROM json_each(?) c
 WHERE NOT EXISTS (SELECT 1 FROM song_media WHERE song_id=json_extract(c.value,'$.id'))`,
	} {
		if _, err := tx.ExecContext(ctx, query, resources); err != nil {
			return err
		}
	}
	return nil
}

func checkSongCatalogState(ctx context.Context, tx *sql.Tx, c Catalog) (bool, error) {
	candidateTime, err := ParseCatalogTime(c.Revision)
	if err != nil {
		return false, err
	}
	var previousRevision, previousSource string
	err = tx.QueryRowContext(ctx, "SELECT revision,source FROM catalog_state WHERE catalog_key='songs_list'").Scan(&previousRevision, &previousSource)
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
		return false, ErrCatalogOlder
	}
	if candidateTime.Equal(previousTime) {
		if IsPrimaryCatalog(c.Source) && !IsPrimaryCatalog(previousSource) {
			return false, nil
		}

		return true, nil
	}
	return false, nil
}

func writeCatalogState(ctx context.Context, tx *sql.Tx, revision, source string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO catalog_state(catalog_key,revision,source) VALUES ('songs_list',?,?)
 ON CONFLICT(catalog_key) DO UPDATE SET revision=excluded.revision,source=excluded.source`, revision, source)
	return err
}

var ErrCatalogOlder = errors.New("清单时间较旧，未更新")

type catalogObservation struct {
	revision, source string
	at               time.Time
}

func (s *Store) observeCatalogVersion(c Catalog) {
	if s.catalogObservations == nil {
		s.catalogObservations = make(map[string]catalogObservation)
	}
	key := c.Source
	at := c.ObservedAt
	if at.IsZero() {
		at = time.Now()
	}
	if previous, ok := s.catalogObservations[key]; ok && !at.After(previous.at) {
		return
	}
	s.catalogObservations[key] = catalogObservation{c.Revision, c.Source, at}
	udon, ok := s.catalogObservations[UdonCatalogURL]
	if !ok {
		return
	}
	for k, backup := range s.catalogObservations {
		if k == UdonCatalogURL || (key != UdonCatalogURL && k != key) || backup.revision <= udon.revision {
			continue
		}
		s.log.Warn("备用清单版本新于 Udon", "event_name", "catalog_backup_newer_than_udon", "source", backup.source, "revision", backup.revision, "observed_at", backup.at, "udon_revision", udon.revision, "udon_observed_at", udon.at)
	}
}
