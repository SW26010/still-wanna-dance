package cacheproxy

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/workfiles"
)

const storageSchemaVersion = 8
const storageApplicationID = 0x53574443 // SWDC
const storageDatabaseName = "storage.sqlite"

// Reading is never permission to initialize a file or to interpret a missing
// database as an empty library. Video availability is checked independently.
func readDatabaseFile(path string) (*sql.DB, error) {
	paths, err := (LibraryPaths{DatabasePath: path}).Resolve()
	if err != nil {
		return nil, err
	}
	path = paths.DatabasePath
	if err := workfiles.CheckDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := validateDatabaseFile(path); err != nil {
		return nil, err
	}
	return connectStorageDatabase(path, "ro", false)
}

// connectStorageDatabase shares the driver and frozen schema contract. Creation
// permission belongs to the caller, never to a business query or schema check.
func connectStorageDatabase(path, mode string, fresh bool) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(1000)", "foreign_keys(1)"}}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if fresh {
		err = initializeStorage(db)
	} else {
		err = validateStorageDatabase(db)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func validateStorageDatabase(db *sql.DB) error {
	var identity, version int
	if err := db.QueryRow("PRAGMA application_id").Scan(&identity); err != nil {
		return err
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if identity != storageApplicationID {
		return fmt.Errorf("无法识别存储数据库；原文件未修改")
	}
	if version > storageSchemaVersion {
		return fmt.Errorf("存储格式版本 %d 较新，请更新程序；原文件未修改", version)
	}
	if version != storageSchemaVersion {
		return fmt.Errorf("不支持存储格式版本 %d；原文件未修改", version)
	}
	return validateStorageSchema(db)
}
