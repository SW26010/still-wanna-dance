package cacheproxy

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/workfiles"
)

const storageSchemaVersion = 4
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

// openOfflineStorage shares the directory lease and format checks with normal
// storage access, without starting an engine, pruning usage or cleaning files.
// New libraries get the standard videos directory for later storage checks.
func openOfflineStorage(root string) (*sql.DB, func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, nil, err
	}
	if err := workfiles.CheckDir(root); err != nil {
		return nil, nil, err
	}
	unlock, err := lockDirectory(filepath.Join(root, ".lock"))
	if err != nil {
		return nil, nil, err
	}
	existing, err := InspectStorage(root)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	db, err := openStorageDatabase(storagePath(root), true)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	close := func() { db.Close(); unlock() }
	if !existing {
		if err := os.MkdirAll(filepath.Join(root, "videos"), 0700); err != nil {
			close()
			return nil, nil, err
		}
	}
	return db, close, nil
}

// Writable opens require the caller to hold the storage directory lock for the
// entire connection lifetime. All schema changes belong to this entry point.
func openStorageDatabase(path string, writable bool) (*sql.DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fresh := false
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("存储数据库不是普通文件")
		}
	} else if !writable || !os.IsNotExist(err) {
		return nil, err
	} else {
		// A surviving media directory is evidence of an initialized library.
		// Losing its database must not silently reset lifetime statistics.
		if filepath.Base(path) == storageDatabaseName {
			if _, statErr := os.Lstat(filepath.Join(filepath.Dir(path), "videos")); statErr == nil {
				return nil, fmt.Errorf("媒体目录仍存在但存储数据库缺失；请恢复完整备份，原目录未修改")
			} else if !os.IsNotExist(statErr) {
				return nil, statErr
			}
		}
		fresh = true
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
