package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"still-wanna-dance/internal/catalogmeta"
)

const songColumns = "id,name,artist,dancer,playerCount,volume,start,end,flip,double_width,skip_random,disable_public,rpe,major,groupName,tag,shader_motion"

const UdonCatalogURL = catalogmeta.UdonURL
const KivaCatalogURL = catalogmeta.KivaURL
const WannaCatalogURL = catalogmeta.WannaURL

// Recognize Udon in persisted catalogs written before the fixed HTTP policy.
func IsUdonCatalog(source string) bool {
	u, err := url.Parse(source)
	return err == nil && strings.EqualFold(u.Hostname(), "api.udon.dance") && u.Path == "/Api/Songs/list"
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
	c, digest, err := normalizeCatalog(c)
	if err != nil {
		return err
	}
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	s.observeCatalogVersion(c)
	tx, err := s.usage.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	duplicate, err := checkSongCatalogState(ctx, tx, c, digest)
	decisionErr := err
	if err != nil {
		if errors.Is(err, ErrCatalogConflict) {
			diagnostic, diagnosticErr := catalogConflictDetails(ctx, tx, c, digest)
			if diagnosticErr != nil {
				s.log.Warn("catalog_conflict_diagnostic_failed", "error", diagnosticErr)
			} else {
				s.log.Warn("catalog_conflict", "revision", c.Revision, "diagnostic", diagnostic)
				return fmt.Errorf("%w：%s", err, diagnostic)
			}
		}
		if !errors.Is(err, ErrCatalogOlder) {
			return err
		}
	}
	// Even an older valid backup can fill holes, without making its placeholders
	// current members or changing already known mappings.
	for _, song := range c.Songs {
		if song.MD5 == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO songs(id) VALUES (?) ON CONFLICT DO NOTHING", song.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO media(md5) VALUES (?) ON CONFLICT DO NOTHING", song.MD5); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO song_media(song_id,md5) VALUES (?,?) ON CONFLICT DO NOTHING", song.ID, song.MD5); err != nil {
			return err
		}
	}
	if errors.Is(decisionErr, ErrCatalogOlder) {
		if err = commitCatalogCheck(ctx, tx, "远端版本较旧，保留歌曲资料并补充资源缺项"); err != nil {
			return err
		}
		s.changed(false)
		return decisionErr
	}
	if duplicate {
		if err = commitCatalogCheck(ctx, tx, "歌曲清单未变化，已检查资源缺项"); err != nil {
			return err
		}
		s.changed(false)
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
	if err := writeCatalogState(ctx, tx, "songs", c.Revision, digest, c.Source); err != nil {
		return err
	}
	if err = commitCatalogCheck(ctx, tx, "已更新本地清单"); err != nil {
		return err
	}
	s.changed(false)
	return nil
}

func checkSongCatalogState(ctx context.Context, tx *sql.Tx, c Catalog, digest string) (bool, error) {
	candidateTime, err := ParseCatalogTime(c.Revision)
	if err != nil {
		return false, err
	}
	var previousRevision, previousDigest, previousSource string
	err = tx.QueryRowContext(ctx, "SELECT revision,digest,source FROM catalog_state WHERE catalog_key='songs'").Scan(&previousRevision, &previousDigest, &previousSource)
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
		if IsUdonCatalog(c.Source) && !IsUdonCatalog(previousSource) {
			return false, nil
		}
		if !IsUdonCatalog(c.Source) && IsUdonCatalog(previousSource) {
			return true, nil
		}
		if digest != previousDigest {
			return false, ErrCatalogConflict
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

var ErrCatalogOlder = errors.New("清单时间较旧，未更新")
var ErrCatalogConflict = errors.New("清单同时间内容冲突，未更新")

type catalogObservation struct {
	revision, source string
	at               time.Time
}

func (s *Store) observeCatalogVersion(c Catalog) {
	if s.catalogObservations == nil {
		s.catalogObservations = make(map[string]catalogObservation)
	}
	key := c.Source
	if IsUdonCatalog(key) {
		key = UdonCatalogURL
	}
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
		s.log.Warn("catalog_backup_newer_than_udon", "source", backup.source, "revision", backup.revision, "observed_at", backup.at, "udon_revision", udon.revision, "udon_observed_at", udon.at)
	}
}

// CatalogNeedsMappings only asks about current members. Historical placeholders
// do not force backup retrieval on every explicit refresh.
func (s *Store) CatalogNeedsMappings(ctx context.Context) (missing bool, resultErr error) {
	if !s.beginRequest() {
		return false, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	resultErr = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_members c
 LEFT JOIN song_media m ON m.song_id=c.song_id WHERE m.md5 IS NULL)`).Scan(&missing)
	return
}

func commitCatalogCheck(ctx context.Context, tx *sql.Tx, message string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO catalog_checks(catalog_key,checked_at,message) VALUES ('songs',?,?)
 ON CONFLICT(catalog_key) DO UPDATE SET checked_at=excluded.checked_at,message=excluded.message,error=''`, time.Now().UnixMilli(), message)
	if err != nil {
		return err
	}
	return tx.Commit()
}
