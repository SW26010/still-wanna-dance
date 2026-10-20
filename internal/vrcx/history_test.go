package vrcx

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, schema string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history # ?.sqlite3")
	// Windows does not allow '?' in filenames.
	path = strings.ReplaceAll(path, "?", "空 格")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db, path
}

const tableSQL = `CREATE TABLE gamelog_video_play(created_at TEXT,video_url TEXT,video_id TEXT)`
const playURL = "https://api.udon.dance/Api/Songs/play?id=1343&node=cf"

func TestSongURLStrictIdentity(t *testing.T) {
	for _, raw := range []string{playURL, "http://API.UDON.DANCE:80/Api/Songs/play?node=nya&id=001343", "https://api.udon.dance/Api/Songs/play?x=1&id=1343"} {
		if id, related := songID(raw); id != 1343 || !related {
			t.Fatalf("valid URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"https://api.udon.dance.evil/Api/Songs/play?id=1343", "https://evil/Api/Songs/play?id=1343",
		"https://api.udon.dance/Api/Songs/playlist?id=1343", "file://api.udon.dance/Api/Songs/play?id=1343",
		"https://api.udon.dance:123/Api/Songs/play?id=1343", "https://user@api.udon.dance/Api/Songs/play?id=1343",
		"https://api.udon.dance/Api/Songs/play?id=-1", "https://api.udon.dance/Api/Songs/play?id=0",
		"https://api.udon.dance/Api/Songs/play?id=+1343", "https://api.udon.dance/Api/Songs/play?id=1.0",
		"https://api.udon.dance/Api/Songs/play?id=9223372036854775808", "https://api.udon.dance/Api/Songs/play?video_id=1343",
		"https://api.udon.dance/Api/Songs/play?id=1&id=1343", "https://api.udon.dance/Api/Songs/play?id=1343&bad=%XX",
	} {
		if id, _ := songID(raw); id != 0 {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestHistorySortTimeAndPrivacy(t *testing.T) {
	db, path := fixture(t, tableSQL)
	for _, row := range [][2]string{
		{"2026-01-01T08:01:00+08:00", playURL},
		{"2026-01-01T00:00:00.123Z", playURL},
		{"2026-01-01T00:00:30Z", "https://api.udon.dance/Api/Songs/play?id=2"},
		{"bad-time", playURL}, {"2999-01-01T00:00:00Z", playURL},
		{"2026-01-01T00:00:00Z", "https://unrelated.invalid/private?secret=private"},
		{"2026-01-01T00:00:00Z", "https://api.udon.dance/Api/Songs/play?id=0"},
	} {
		if _, err := db.Exec("INSERT INTO gamelog_video_play VALUES (?,?,'999')", row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	var got []event
	summary, err := Read(context.Background(), path, func(id, at int64) { got = append(got, event{id, at}) })
	if err != nil {
		t.Fatal(err)
	}
	if summary != (Summary{Total: 7, Recognized: 5, Invalid: 3, Unrelated: 1}) || len(got) != 3 {
		t.Fatal(summary, got)
	}
	if got[0].id != 1343 || got[1].id != 2 || got[2].id != 1343 || got[2].at-got[0].at != 59877 {
		t.Fatal(got)
	}
}

func TestHistoryWALSnapshotReadOnly(t *testing.T) {
	db, path := fixture(t, "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; "+tableSQL)
	if _, err := db.Exec("INSERT INTO gamelog_video_play VALUES ('2026-01-01T00:00:00Z',?,'ignored')", playURL); err != nil {
		t.Fatal(err)
	}
	hash := func(path string) [32]byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(data)
	}
	beforeDB, beforeWAL := hash(path), hash(path+"-wal")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO gamelog_video_play VALUES ('2026-01-02T00:00:00Z',?,'ignored')", playURL); err != nil {
		t.Fatal(err)
	}
	var got []event
	_, err = Read(context.Background(), path, func(id, at int64) { got = append(got, event{id, at}) })
	if err != nil || len(got) != 1 {
		t.Fatal("must read committed WAL, excluding active write transaction", got, err)
	}
	if hash(path) != beforeDB || hash(path+"-wal") != beforeWAL {
		t.Fatal("reader modified database or WAL")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got = nil
	_, err = Read(context.Background(), path, func(id, at int64) { got = append(got, event{id, at}) })
	if err != nil || len(got) != 2 {
		t.Fatal("new snapshot missing committed event", got, err)
	}
}

func TestHistoryFailuresProduceNoObservations(t *testing.T) {
	for _, schema := range []string{"CREATE TABLE unrelated(x)", "CREATE TABLE gamelog_video_play(created_at)", "CREATE VIEW gamelog_video_play AS SELECT 'secret' AS created_at, 'secret' AS video_url"} {
		_, path := fixture(t, schema)
		_, err := Read(context.Background(), path, func(int64, int64) { t.Fatal("unexpected observation") })
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	_, path := fixture(t, tableSQL)
	if err := os.WriteFile(path+".corrupt", []byte("private-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{path + ".missing", path + ".corrupt"} {
		_, err := Read(context.Background(), bad, func(int64, int64) { t.Fatal("unexpected observation") })
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".missing"); !os.IsNotExist(err) {
		t.Fatal("reader created missing source")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Read(ctx, path, func(int64, int64) { t.Fatal("canceled read") })
	if err == nil {
		t.Fatal("cancellation lost")
	}
}

func TestHistoryLargeInputAndLimit(t *testing.T) {
	db, path := fixture(t, tableSQL)
	_, err := db.Exec(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<10000)
		INSERT INTO gamelog_video_play SELECT '2026-01-01T00:00:00Z',?,'ignored' FROM seq`, playURL)
	if err != nil {
		t.Fatal(err)
	}
	var observations int
	summary, err := Read(context.Background(), path, func(int64, int64) { observations++ })
	if err != nil || observations != 10000 || summary.Total != 10000 {
		t.Fatal(summary, observations, err)
	}
	before, _ := os.ReadDir(filepath.Dir(path))
	_, err = read(context.Background(), path, time.Now(), 9999, func(int64, int64) { t.Fatal("partial read escaped limit") })
	if err == nil {
		t.Fatal("unbounded input")
	}
	after, _ := os.ReadDir(filepath.Dir(path))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reader created persistent files")
	}
}

func TestHistoryBusyDoesNotReadPartialState(t *testing.T) {
	db, path := fixture(t, tableSQL)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err = Read(ctx, path, func(int64, int64) { t.Fatal("read locked source") }); err == nil {
		t.Fatal("exclusive lock ignored")
	}
}
