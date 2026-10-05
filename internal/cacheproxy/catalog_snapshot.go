package cacheproxy

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"time"
)

// CatalogStatus describes the shared Kiva/WannaInfo MD5 catalog. A zero check time
// means this database predates check tracking or has never passed validation.
type CatalogStatus struct {
	Revision  string    `json:"revision"`
	CheckedAt time.Time `json:"checkedAt"`
	Message   string    `json:"message"`
	Error     string    `json:"error"`
}

func (s *Server) catalogStatus(ctx context.Context) (CatalogStatus, error) {
	return readCatalogStatus(ctx, s.usage.db)
}

// LoadCatalogStatus reads persisted status without starting an engine or creating
// a database. Databases predating check tracking retain an unknown check time.
func LoadCatalogStatus(ctx context.Context, root string) (CatalogStatus, error) {
	db, err := cacheReadDB(root)
	if os.IsNotExist(err) {
		return CatalogStatus{}, nil
	}
	if err != nil {
		return CatalogStatus{}, err
	}
	defer db.Close()
	var hasChecks bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='catalog_checks')").Scan(&hasChecks); err != nil {
		return CatalogStatus{}, err
	}
	if !hasChecks {
		var status CatalogStatus
		err := db.QueryRowContext(ctx, "SELECT COALESCE((SELECT revision FROM catalog_state WHERE catalog_key='songs'),'')").Scan(&status.Revision)
		return status, err
	}
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
		v.CheckedAt = time.UnixMilli(checked)
	}
	return v, err
}

func (s *Server) CatalogStatus(ctx context.Context) (CatalogStatus, error) {
	if !s.beginRequest() {
		return CatalogStatus{}, context.Canceled
	}
	defer s.wg.Done()
	return s.catalogStatus(ctx)
}

func (s *Server) RecordCatalogError(ctx context.Context, err error) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
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
		rows, queryErr := s.usage.db.QueryContext(ctx, `SELECT s.song_id,s.name,m.md5,c.song_id IS NOT NULL FROM songs s
 JOIN song_media m ON m.song_id=s.song_id LEFT JOIN catalog_members c ON c.song_id=s.song_id ORDER BY s.song_id`)
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
				refs[strconv.FormatInt(song.ID, 10)] = song.MD5
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
		result.Files, err = CheckReferencedFiles(ctx, s.cfg.StorageDir, refs)
	}
	if err != nil {
		release()
		return result, func() {}, err
	}
	return result, release, nil
}
