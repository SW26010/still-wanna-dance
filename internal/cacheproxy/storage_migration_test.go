package cacheproxy

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

func migrationFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/storage-v8.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "migration.sqlite")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	// The frozen structural fixture intentionally contains a placeholder URL
	// without e/s. Keep it frozen; successful migration cases use a real-shaped
	// address in this isolated copy. The unmodified placeholder is tested below.
	if _, err := db.Exec("UPDATE song_urls SET url='https://example.com/files/1/42-video.mp4?e='||md5||'&s='||byte_size"); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func migrationRows(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for rows.Next() {
		values, args := make([]any, len(columns)), make([]any, len(columns))
		for i := range args {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&out, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestStorageV9MigrationPreservesData(t *testing.T) {
	db, _ := migrationFixture(t)
	queries := []string{
		"SELECT * FROM songs ORDER BY id", "SELECT * FROM song_media ORDER BY song_id",
		"SELECT * FROM song_preferences ORDER BY song_id", "SELECT * FROM song_usage ORDER BY song_id",
		"SELECT * FROM catalog_members ORDER BY song_id", "SELECT * FROM media_access ORDER BY md5",
		"SELECT event_id,resource_md5,requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,song_id,completed_at,response_bytes,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes FROM request_events ORDER BY event_id",
	}
	before := make([]string, len(queries))
	for i, query := range queries {
		before[i] = migrationRows(t, db, query)
	}
	if _, err := db.Exec(`INSERT INTO media VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',0);
 INSERT INTO media VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',NULL);
 INSERT INTO songs(id) VALUES(43),(44);
 INSERT INTO song_media VALUES(43,'0123456789abcdef0123456789abcdef');
 INSERT INTO song_urls SELECT 43,api,node,url||'&sample=new',md5,byte_size,1,observed_at+1,99 FROM song_urls WHERE song_id=42;
 INSERT INTO song_urls SELECT 44,api,node,url||'&sample=tie',md5,byte_size,1,observed_at,99 FROM song_urls WHERE song_id=43;`); err != nil {
		t.Fatal(err)
	}
	// Retain the updated fixtures too, including shared resource references.
	for i, query := range queries {
		before[i] = migrationRows(t, db, query)
	}
	wantURL := migrationRows(t, db, "SELECT url FROM song_urls WHERE song_id=44")
	wantState := migrationRows(t, db, "SELECT revision,source FROM catalog_state WHERE catalog_key='songs'")
	if err := migrateStorageV8(db); err != nil {
		t.Fatal(err)
	}
	for i, query := range queries {
		if got := migrationRows(t, db, query); got != before[i] {
			t.Fatalf("changed retained data: %s\n%s\n%s", query, got, before[i])
		}
	}
	if got := migrationRows(t, db, "SELECT url FROM media_urls"); got != wantURL {
		t.Fatal("duplicate selection", got, wantURL)
	}
	if got := migrationRows(t, db, "SELECT revision,source FROM catalog_state WHERE catalog_key='songs_list'"); got != wantState {
		t.Fatal(got, wantState)
	}
	if got := migrationRows(t, db, "SELECT byte_size FROM media WHERE md5 IN ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')"); got != "[<nil>]\n[<nil>]\n" {
		t.Fatal(got)
	}
	expected, err := expectedV9Schema()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSchemaObjects(db, expected, 9); err != nil {
		t.Fatal(err)
	}
	if got := migrationRows(t, db, "PRAGMA foreign_keys"); got != "[1]\n" {
		t.Fatal("foreign keys not restored", got)
	}
	if got := migrationRows(t, db, "PRAGMA user_version"); got != "[9]\n" {
		t.Fatal(got)
	}
}

func TestStorageV9MigrationRollbackAtEveryStep(t *testing.T) {
	statements := append(storageV9MigrationSteps(), "PRAGMA quick_check(1)", "SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_check)", "INSERT INTO media_urls", "SELECT count(*) FROM pragma_foreign_key_check", "PRAGMA user_version=9")
	for step, statement := range statements {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			fixture, path := migrationFixture(t)
			fixture.Close()
			fault := &migrationFaultConnector{path: path, statement: statement, cause: errors.New("injected migration write failure")}
			db := sql.OpenDB(fault)
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { db.Close() })
			before := snapshotBusinessTables(t, db)
			// The real migration owns transaction rollback and foreign-key
			// restoration. Only one driver Exec fails; all other I/O is SQLite.
			if err := migrateStorageV8(db); !errors.Is(err, fault.cause) || !fault.triggered {
				t.Fatal("production migration did not reach the injected failure", err)
			}
			expected, _ := expectedV8Schema()
			if err := validateSchemaObjects(db, expected, 8); err != nil {
				t.Fatal(err)
			}
			if got := snapshotBusinessTables(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("rollback changed data")
			}
			if got := migrationRows(t, db, "PRAGMA user_version"); got != "[8]\n" {
				t.Fatal("failed migration changed version", got)
			}
			if got := migrationRows(t, db, "PRAGMA foreign_keys"); got != "[1]\n" {
				t.Fatal("production migration did not restore foreign keys", got)
			}
			if err := migrateStorageV8(db); err != nil {
				t.Fatal("retry", err)
			}
		})
	}
}

// A test-only connector intercepts one write without replacing migration code
// or adding a mutable production hook. The real connection stays pinned by sql.DB.
type migrationFaultConnector struct {
	path, statement string
	cause           error
	triggered       bool
}

func (c *migrationFaultConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *migrationFaultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(filepath.ToSlash(c.path) + "?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	return &migrationFaultConn{Conn: conn, fault: c}, nil
}

type migrationFaultConn struct {
	driver.Conn
	fault *migrationFaultConnector
}

func (c *migrationFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.fault.triggered && strings.HasPrefix(query, c.fault.statement) {
		c.fault.triggered = true
		return nil, c.fault.cause
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *migrationFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !c.fault.triggered && strings.HasPrefix(query, c.fault.statement) {
		c.fault.triggered = true
		return nil, c.fault.cause
	}
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func TestStorageV9MigrationRejectsInvalidDataWithoutURLLeak(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE song_urls SET url='https://example.invalid/video'",
		"UPDATE song_urls SET url='http://secret.invalid/video.mp4?e=bad&s=12&private=secret'",
		"UPDATE song_urls SET node=''",
		"UPDATE song_urls SET md5='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'",
		"UPDATE song_media SET md5='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'",
	} {
		t.Run(mutation, func(t *testing.T) {
			db, _ := migrationFixture(t)
			if _, err := db.Exec("PRAGMA foreign_keys=OFF;" + mutation + ";PRAGMA foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			before := snapshotBusinessTables(t, db)
			err := migrateStorageV8(db)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("must fail without URL", err)
			}
			if got := snapshotBusinessTables(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("failed migration changed data")
			}
			if got := migrationRows(t, db, "PRAGMA user_version"); got != "[8]\n" {
				t.Fatal(got)
			}
			if got := migrationRows(t, db, "PRAGMA foreign_keys"); got != "[1]\n" {
				t.Fatal(got)
			}
		})
	}
}

func TestStorageV9MigrationRejectsBrokenCheckConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, repair string
	}{
		{"retained", "INSERT INTO song_usage VALUES (42,-1,1,1) ON CONFLICT(song_id) DO UPDATE SET demand_count=-1", "UPDATE song_usage SET demand_count=0 WHERE song_id=42"},
		{"removed", "INSERT INTO song_playback VALUES (42,-1,1) ON CONFLICT(song_id) DO UPDATE SET transfer_count=-1", "UPDATE song_playback SET transfer_count=0 WHERE song_id=42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := migrationFixture(t)
			if _, err := db.Exec("PRAGMA ignore_check_constraints=ON; " + tc.mutation + "; PRAGMA ignore_check_constraints=OFF"); err != nil {
				t.Fatal(err)
			}
			before := snapshotBusinessTables(t, db)
			beforeFile, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := migrateStorageV8(db); err == nil {
				t.Fatal("migration accepted invalid v8 data")
			}
			afterFile, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(afterFile, beforeFile) {
				t.Fatal("failed migration changed the v8 file", err)
			}
			expected, _ := expectedV8Schema()
			if err := validateSchemaObjects(db, expected, 8); err != nil {
				t.Fatal(err)
			}
			if got := snapshotBusinessTables(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("failed migration changed data")
			}
			if got := migrationRows(t, db, "PRAGMA user_version"); got != "[8]\n" {
				t.Fatal("failed migration changed version", got)
			}
			if got := migrationRows(t, db, "PRAGMA foreign_keys"); got != "[1]\n" {
				t.Fatal("failed migration did not restore foreign keys", got)
			}
			if _, err := db.Exec(tc.repair); err != nil {
				t.Fatal(err)
			}
			if err := migrateStorageV8(db); err != nil {
				t.Fatal("migration after repair", err)
			}
		})
	}
}

func TestStorageV9MigrationRejectsBrokenRemovedTableForeignKey(t *testing.T) {
	db, path := migrationFixture(t)
	if _, err := db.Exec("PRAGMA foreign_keys=OFF; UPDATE song_urls SET song_id=999 WHERE song_id=42; PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	var broken int
	if err := db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil || broken == 0 {
		t.Fatalf("fixture has no broken foreign key: count=%d error=%v", broken, err)
	}
	before := snapshotBusinessTables(t, db)
	beforeFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateStorageV8(db); err == nil {
		t.Fatal("migration accepted a broken foreign key in a removed table")
	}
	afterFile, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(afterFile, beforeFile) {
		t.Fatal("failed migration changed the v8 file", err)
	}
	expected, _ := expectedV8Schema()
	if err := validateSchemaObjects(db, expected, 8); err != nil {
		t.Fatal(err)
	}
	if got := snapshotBusinessTables(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration changed v8 data")
	}
	if got := migrationRows(t, db, "PRAGMA foreign_keys"); got != "[1]\n" {
		t.Fatal("failed migration did not restore foreign keys", got)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF; UPDATE song_urls SET song_id=42 WHERE song_id=999; PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if err := migrateStorageV8(db); err != nil {
		t.Fatal("migration after repair", err)
	}
}
