package cacheproxy

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
)

// These contracts are frozen independently of the current business schema.
//
//go:embed schema/storage-v8.sql
var storageV8SQL string

//go:embed schema/storage-v9.sql
var storageV9SQL string

func expectedSchemaFor(ddl string) func() (map[string]string, error) {
	return sync.OnceValues(func() (map[string]string, error) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		defer db.Close()
		if _, err := db.Exec(ddl); err != nil {
			return nil, err
		}
		return schemaObjects(db)
	})
}

var expectedV8Schema = expectedSchemaFor(storageV8SQL)
var expectedV9Schema = expectedSchemaFor(storageV9SQL)

// migrateStorageV8 runs only at the writable open boundary, while the caller
// owns the database file lease. Pin the connection so foreign_keys is restored
// on the same connection after either commit or rollback.
func migrateStorageV8(db *sql.DB) (resultErr error) {
	expected, err := expectedV8Schema()
	if err != nil {
		return err
	}
	if err := validateSchemaObjects(db, expected, 8); err != nil {
		return err
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		_, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		resultErr = errors.Join(resultErr, err)
	}()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var integrity string
	if err := tx.QueryRow("PRAGMA quick_check(1)").Scan(&integrity); err != nil {
		return fmt.Errorf("检查旧存储完整性失败: %w", err)
	}
	if integrity != "ok" {
		return errors.New("旧存储数据不满足约束或已损坏；原文件未修改")
	}
	var brokenOld int
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_check)").Scan(&brokenOld); err != nil {
		return fmt.Errorf("检查旧存储关联失败: %w", err)
	}
	if brokenOld != 0 {
		return errors.New("旧存储关联不完整；原文件未修改")
	}
	urls, err := readV8MediaURLs(tx)
	if err != nil {
		return err
	}
	for _, statement := range storageV9MigrationSteps() {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("升级存储结构失败: %w", err)
		}
	}
	for _, u := range urls {
		if _, err := tx.Exec(`INSERT INTO media_urls(md5,node,url) VALUES (?,?,?)
 ON CONFLICT(md5,node) DO UPDATE SET url=excluded.url`, u.md5, u.node, u.url); err != nil {
			return fmt.Errorf("升级媒体地址失败: %w", err)
		}
	}
	expected, err = expectedV9Schema()
	if err != nil {
		return err
	}
	if err := validateSchemaObjects(tx, expected, 9); err != nil {
		return err
	}
	var broken int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil {
		return err
	}
	if broken != 0 {
		return errors.New("存储升级后的关联不完整；原文件未修改")
	}
	if _, err := tx.Exec("PRAGMA user_version=9"); err != nil {
		return err
	}
	return tx.Commit()
}

type migratedMediaURL struct{ md5, node, url string }

func readV8MediaURLs(tx *sql.Tx) ([]migratedMediaURL, error) {
	// Ascending order followed by UPSERT selects the latest acquisition. Ties
	// use the frozen v8 primary key. Age and failed_at are intentionally ignored.
	rows, err := tx.Query("SELECT md5,node,url FROM song_urls ORDER BY observed_at,song_id,api,node")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []migratedMediaURL
	for rows.Next() {
		var u migratedMediaURL
		if err := rows.Scan(&u.md5, &u.node, &u.url); err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodGet, u.url, nil)
		if err != nil {
			return nil, errors.New("旧媒体地址格式无效；原文件未修改")
		}
		v, err := parseVideoURL(req, math.MaxInt64-1)
		if err != nil || v.key != u.md5 || strings.TrimSpace(u.node) == "" {
			return nil, errors.New("旧媒体地址与资源或节点不符；原文件未修改")
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func storageV9MigrationSteps() []string {
	const eventColumns = "event_id,resource_md5,requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,song_id,completed_at,response_bytes,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes"
	steps := []string{
		"CREATE TEMP TABLE migrating_media AS SELECT md5,NULLIF(byte_size,0) AS byte_size FROM media",
		"CREATE TEMP TABLE migrating_catalog AS SELECT 'songs_list' AS catalog_key,revision,source FROM catalog_state WHERE catalog_key='songs'",
		"CREATE TEMP TABLE migrating_events AS SELECT " + eventColumns + " FROM request_events",
	}
	for _, table := range []string{"song_urls", "song_playback", "traffic_totals", "catalog_checks", "request_observation", "media", "catalog_state", "request_events"} {
		steps = append(steps, "DROP TABLE "+table)
	}
	// Use the canonical CREATE statements, not table renames (which rewrite
	// foreign-key targets and leave different quoted DDL in sqlite_schema).
	for _, statement := range strings.Split(storageV9SQL, ";") {
		statement = strings.TrimSpace(statement)
		for _, prefix := range []string{"CREATE TABLE media (", "CREATE TABLE media_urls (", "CREATE TABLE catalog_state (", "CREATE TABLE request_events (", "CREATE INDEX request_events_"} {
			if strings.HasPrefix(statement, prefix) {
				steps = append(steps, statement)
				break
			}
		}
	}
	return append(steps,
		"INSERT INTO media SELECT * FROM migrating_media",
		"INSERT INTO catalog_state SELECT * FROM migrating_catalog",
		"INSERT INTO request_events("+eventColumns+") SELECT * FROM migrating_events",
		"DROP TABLE migrating_media", "DROP TABLE migrating_catalog", "DROP TABLE migrating_events",
	)
}
