package cacheproxy

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// CatalogStatus describes the accepted song catalog. A zero check time
// means this database has never passed validation.
type CatalogStatus struct {
	Revision  string    `json:"revision"`
	CheckedAt time.Time `json:"checkedAt"`
	Message   string    `json:"message"`
	Error     string    `json:"error"`
}

func (s *Store) catalogStatus(ctx context.Context) (CatalogStatus, error) {
	return readCatalogStatus(ctx, s.usage.db)
}

// LoadCatalogStatus reads persisted status without starting an engine or creating
// a database. Existing databases must match the frozen schema.
func LoadCatalogStatus(ctx context.Context, databasePath string) (CatalogStatus, error) {
	db, err := readDatabaseFile(databasePath)
	if err != nil {
		return CatalogStatus{}, err
	}
	defer db.Close()
	return readCatalogStatus(ctx, db)
}

func readCatalogStatus(ctx context.Context, db *sql.DB) (CatalogStatus, error) {
	var v CatalogStatus
	var checked int64
	err := db.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT revision FROM catalog_state WHERE catalog_key='songs'),''),
 COALESCE((SELECT checked_at FROM catalog_checks WHERE catalog_key='songs'),0),
 COALESCE((SELECT message FROM catalog_checks WHERE catalog_key='songs'),''),
 COALESCE((SELECT error FROM catalog_checks WHERE catalog_key='songs'),'')`).Scan(&v.Revision, &checked, &v.Message, &v.Error)
	if checked != 0 {
		v.CheckedAt = time.UnixMilli(checked).UTC()
	}
	return v, err
}

func (s *Store) CatalogStatus(ctx context.Context) (resultValue CatalogStatus, resultErr error) {
	if !s.beginRequest() {
		return CatalogStatus{}, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	return s.catalogStatus(ctx)
}

func (s *Store) RecordCatalogError(ctx context.Context, err error) (resultErr error) {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	_, saveErr := s.usage.db.ExecContext(ctx, `INSERT INTO catalog_checks(catalog_key,error) VALUES ('songs',?)
 ON CONFLICT(catalog_key) DO UPDATE SET error=excluded.error`, err.Error())
	return saveErr
}

type LocalCatalog struct {
	Current map[string]bool
	Status  CatalogStatus
	Songs   []CatalogSong
	Files   ReferenceCheck
}

// ReadLocalCatalog copies watermarks and mappings under the mapping lock, then
// pins those exact resources until release. Callers must not sync this partial
// projection back as a remote catalog.
func (s *Server) ReadLocalCatalog(ctx context.Context) (LocalCatalog, func(), error) {
	return s.readLocalCatalog(ctx, nil)
}

// DownloadTarget identifies one current song or one currently associated resource.
// A song without a known resource may be resolved by the normal download path.
type DownloadTarget struct {
	SongID   string `json:"songID,omitempty"`
	Resource string `json:"resource,omitempty"`
}

func (s *Server) ReadDownloadTarget(ctx context.Context, target DownloadTarget) (LocalCatalog, func(), error) {
	return s.readLocalCatalog(ctx, &target)
}

func (s *Server) readLocalCatalog(ctx context.Context, target *DownloadTarget) (LocalCatalog, func(), error) {
	if !s.beginRequest() {
		return LocalCatalog{}, func() {}, context.Canceled
	}
	var result LocalCatalog
	result.Current = map[string]bool{}
	refs := map[string]string{}
	keys := map[string]bool{}
	s.mappingMu.Lock()
	status, err := s.catalogStatus(ctx)
	result.Status = status
	if err == nil {
		query := `SELECT s.id,s.name,COALESCE(m.md5,''),c.song_id IS NOT NULL FROM songs s
 LEFT JOIN song_media m ON m.song_id=s.id LEFT JOIN catalog_members c ON c.song_id=s.id`
		var args []any
		if target == nil {
			query += " WHERE m.md5 IS NOT NULL"
		} else {
			query += " WHERE (?<>'' AND s.id=?) OR (?<>'' AND m.md5=?)"
			args = []any{target.SongID, target.SongID, target.Resource, target.Resource}
		}
		rows, queryErr := s.usage.db.QueryContext(ctx, query+" ORDER BY s.id", args...)
		err = queryErr
		if err == nil {
			for rows.Next() {
				var song CatalogSong
				var current bool
				if err = rows.Scan(&song.ID, &song.Name, &song.MD5, &current); err != nil {
					break
				}
				result.Songs = append(result.Songs, song)
				result.Current[strconv.FormatInt(song.ID, 10)] = current
				if song.MD5 != "" {
					refs[strconv.FormatInt(song.ID, 10)] = song.MD5
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
	}
	if err == nil {
		for _, key := range refs {
			if !keys[key] {
				s.pinVideo(video{key: key})
				keys[key] = true
			}
		}
	}
	s.mappingMu.Unlock()
	release := func() {
		for key := range keys {
			s.releaseVideo(video{key: key})
		}
		s.wg.Done()
	}
	if err == nil {
		result.Files, err = CheckReferencedFiles(ctx, s.cfg.videosDir(), refs)
		// Unresolved songs still use the same job/resolve/download mechanism,
		// but must not be presented to the complete-file checker as a fake MD5.
		if err == nil && target != nil {
			for _, song := range result.Songs {
				if song.MD5 == "" {
					result.Files.References[strconv.FormatInt(song.ID, 10)] = ""
				}
			}
		}
	}
	if err != nil {
		release()
		return result, func() {}, err
	}
	return result, release, nil
}
