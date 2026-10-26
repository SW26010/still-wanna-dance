package cacheproxy

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"sync"
)

//go:embed schema/storage-v7.sql
var storageSchemaSQL string

// Only a newly created database reaches this transaction. Existing databases
// are validated, never repaired. Future migrations belong at the open boundary.
func initializeStorage(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(storageSchemaSQL + `INSERT INTO traffic_totals(id,saved_bytes,hits,misses) VALUES(1,0,0,0);`); err != nil {
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d;", storageApplicationID, storageSchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// SQLite's own normalized DDL includes columns, defaults, constraints, indexes,
// triggers and views. Compare against the frozen schema, ignoring whitespace only.
func schemaObjects(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make(map[string]string)
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			return nil, err
		}
		objects[kind+":"+name] = strings.Join(strings.Fields(ddl), " ")
	}
	return objects, rows.Err()
}

var expectedSchema = sync.OnceValues(func() (map[string]string, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := db.Exec(storageSchemaSQL); err != nil {
		return nil, err
	}
	return schemaObjects(db)
})

func validateStorageSchema(db *sql.DB) error {
	expected, err := expectedSchema()
	if err != nil {
		return err
	}
	actual, err := schemaObjects(db)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("存储结构与版本 %d 不符；原文件未修改", storageSchemaVersion)
	}
	for name, ddl := range expected {
		if actual[name] != ddl {
			return fmt.Errorf("存储结构 %s 与版本 %d 不符；原文件未修改", name, storageSchemaVersion)
		}
	}
	return nil
}
