package cacheproxy

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A test-only next format removes a diagnostic field by rebuilding its table.
// This exercises the frozen v7 baseline, transaction/version rollback, exact
// fresh-vs-upgraded DDL and downgrade rejection. It is not a shipped migration
// or a promise to accept development-era v5 databases.
func TestStorageUpgradeRehearsal(t *testing.T) {
	golden, err := os.ReadFile("testdata/storage-v7.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	nextSchema := strings.Replace(storageSchemaSQL, " host TEXT NOT NULL,\n", "", 1)
	if nextSchema == storageSchemaSQL {
		t.Fatal("rehearsal must change the request table")
	}
	fresh, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Exec(nextSchema); err != nil {
		t.Fatal(err)
	}
	expectedSchema, err := schemaObjects(fresh)
	if err != nil {
		t.Fatal(err)
	}
	steps := []string{
		`ALTER TABLE request_events RENAME TO upgrading_requests`,
		`DROP INDEX request_events_resource_time; DROP INDEX request_events_time`,
	}
	// Use the exact target CREATE statements. Renaming a temporary new table
	// into place would leave quoted DDL different from a fresh database.
	for _, statement := range strings.Split(nextSchema, ";") {
		statement = strings.TrimSpace(statement)
		if strings.HasPrefix(statement, "CREATE TABLE request_events (") || strings.HasPrefix(statement, "CREATE INDEX request_events_") {
			steps = append(steps, statement)
		}
	}
	const columns = "event_id,resource_md5,requested_at,method,range_header,cache_result,outcome,file_bytes,transferred_bytes,elapsed_ms,status,song_id,completed_at,response_bytes,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes"
	steps = append(steps,
		"INSERT INTO request_events("+columns+") SELECT "+columns+" FROM upgrading_requests",
		"DROP TABLE upgrading_requests",
		fmt.Sprintf("PRAGMA user_version=%d", storageSchemaVersion+1),
	)
	upgrade := func(db *sql.DB, failAfter int) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for i, statement := range steps {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
			if i == failAfter {
				return fmt.Errorf("injected failure after step %d", i)
			}
		}
		return tx.Commit()
	}
	for failAfter := -1; failAfter < len(steps); failAfter++ {
		t.Run(fmt.Sprintf("fail-after-%d", failAfter), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "personal.sqlite")
			if err := os.WriteFile(path, golden, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			before := snapshotBusinessTables(t, store.db)
			err = upgrade(store.db, failAfter)
			if failAfter >= 0 {
				if err == nil || !strings.Contains(err.Error(), "injected failure") {
					t.Fatal("did not reach the injected failure", err)
				}
				if err := validateStorageDatabase(store.db); err != nil {
					t.Fatal("rollback left an invalid v7 format", err)
				}
				if got := snapshotBusinessTables(t, store.db); !reflect.DeepEqual(got, before) {
					t.Fatal("rollback changed business data")
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, golden) {
					t.Fatal("failed upgrade changed database bytes", err)
				}
				store, err = OpenStore(path)
				if err != nil {
					t.Fatal("failed upgrade cannot reopen", err)
				}
				if err := upgrade(store.db, -1); err != nil {
					t.Fatal("retry failed", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			gotSchema, err := schemaObjects(store.db)
			if err != nil || !reflect.DeepEqual(gotSchema, expectedSchema) {
				t.Fatal("upgraded DDL differs from a fresh next-format database", gotSchema, expectedSchema, err)
			}
			// Remove only the intentionally dropped column from the expected
			// snapshot; RawMessage keeps 64-bit timestamps and IDs lossless.
			var requestSnapshot struct {
				Columns []string
				Values  [][]json.RawMessage
			}
			if err := json.Unmarshal([]byte(before["request_events"]), &requestSnapshot); err != nil {
				t.Fatal(err)
			}
			for i, column := range requestSnapshot.Columns {
				if column == "host" {
					requestSnapshot.Columns = append(requestSnapshot.Columns[:i], requestSnapshot.Columns[i+1:]...)
					for j, row := range requestSnapshot.Values {
						requestSnapshot.Values[j] = append(row[:i], row[i+1:]...)
					}
					break
				}
			}
			encoded, err := json.Marshal(requestSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			before["request_events"] = string(encoded)
			if got := snapshotBusinessTables(t, store.db); !reflect.DeepEqual(got, before) {
				t.Fatal("upgrade changed surviving data")
			}
			var integrity string
			if err := store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatal(integrity, err)
			}
			var broken int
			if err := store.db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil || broken != 0 {
				t.Fatal("upgrade broke relations", broken, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, writable := range []bool{false, true} {
				db, err := openFixtureDatabase(path, writable)
				if db != nil {
					db.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "较新") {
					t.Fatal("v7 must reject the next format", err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, upgraded) {
				t.Fatal("downgrade attempt changed upgraded data", err)
			}
		})
	}
}
