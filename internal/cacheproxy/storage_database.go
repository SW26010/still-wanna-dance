package cacheproxy

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/workfiles"
)

const storageSchemaVersion = 9
const storageApplicationID = 0x53574443 // SWDC
const storageDatabaseName = "storage.sqlite"

// Reading is never permission to initialize a file or to interpret a missing
// database as an empty library. Video availability is checked independently.
func readDatabaseFile(path string) (*sql.DB, error) {
	return inspectDatabaseFile(path, false)
}

// CheckDatabaseSelection checks format eligibility without reading business data
// or migrating a selected v8 file. Actual opening repeats validation.
func CheckDatabaseSelection(path string) error {
	db, err := inspectDatabaseFile(path, true)
	if err == nil {
		err = db.Close()
	}
	return err
}

func inspectDatabaseFile(path string, selectable bool) (*sql.DB, error) {
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
	return connectStorageDatabaseForSelection(path, "ro", false, selectable)
}

// connectStorageDatabase shares the driver and frozen schema contract. Creation
// permission belongs to the caller, never to a business query or schema check.
func connectStorageDatabase(path, mode string, fresh bool) (*sql.DB, error) {
	return connectStorageDatabaseForSelection(path, mode, fresh, false)
}

func connectStorageDatabaseForSelection(path, mode string, fresh, selectable bool) (*sql.DB, error) {
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
		err = validateStorageDatabaseForOpen(db, mode == "rw", selectable)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func validateStorageDatabase(db *sql.DB) error {
	return validateStorageDatabaseForOpen(db, false, false)
}

func validateStorageDatabaseForOpen(db *sql.DB, migrate, selectable bool) error {
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
	if version == 8 && (migrate || selectable) {
		if migrate {
			return migrateStorageV8(db)
		}
		expected, err := expectedV8Schema()
		if err != nil {
			return err
		}
		return validateSchemaObjects(db, expected, 8)
	}
	if version != storageSchemaVersion {
		return fmt.Errorf("不支持存储格式版本 %d；原文件未修改", version)
	}
	return validateStorageSchema(db)
}
