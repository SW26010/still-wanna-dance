package cacheproxy

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const storageSchemaVersion = 1
const storageApplicationID = 0x53574443 // SWDC
const storageDatabaseName = "storage.sqlite"

func storagePath(root string) string { return filepath.Join(root, storageDatabaseName) }

// A missing store is empty state for readers, never a reason to create a file.
func readStorageDatabase(root string) (*sql.DB, error) {
	path := storagePath(root)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return openStorageDatabase(path, false)
}

// Writable opens require the caller to hold the storage directory lock for the
// entire connection lifetime. All schema changes belong to this entry point.
func openStorageDatabase(path string, writable bool) (*sql.DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("存储数据库不是普通文件")
		}
	} else if !writable || !os.IsNotExist(err) {
		return nil, err
	}
	if writable {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	mode := "ro"
	if writable {
		mode = "rwc"
	}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(1000)", "foreign_keys(1)"}}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = validateStorageDatabase(db, writable); err == nil && writable {
		err = initializeStorage(db)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func validateStorageDatabase(db *sql.DB, allowEmpty bool) error {
	var identity, version, objects int
	if err := db.QueryRow("PRAGMA application_id").Scan(&identity); err != nil {
		return err
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return err
	}
	if allowEmpty && identity == 0 && version == 0 && objects == 0 {
		return nil
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
	return nil
}
