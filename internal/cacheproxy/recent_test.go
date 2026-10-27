package cacheproxy

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecentRequestsWindowAndNames(t *testing.T) {
	root := t.TempDir()
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	_, err = u.db.Exec(`INSERT INTO songs(song_id,name) VALUES ('1','共享歌曲'),('2','');
 INSERT OR IGNORE INTO media(md5) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'); INSERT INTO song_media(song_id,md5) VALUES ('1','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'),('2','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	err = u.write([]usageEvent{
		{key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", at: now, method: "GET", outcome: "completed", status: 200, size: 100, bytes: 100},
		{at: now + 1, method: "GET", outcome: "canceled", status: 200, bytes: 3, rangeHeader: "bytes=0-9"},
		{key: strings.Repeat("b", 32), at: now + 3, method: "GET", outcome: "completed", status: 200, size: 100, bytes: 5, rangeHeader: "secret=signed-url"},
		{key: strings.Repeat("c", 32), at: now + 3, method: "GET", outcome: "completed", status: 206, size: 100, bytes: 10, rangeHeader: "bytes=0-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReadRecentRequests(context.Background(), storagePath(root), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasMore || len(result.Requests) != 2 || result.Requests[0].Resource != strings.Repeat("c", 32) || result.Requests[1].Outcome != "completed" {
		t.Fatalf("page: %+v", result)
	}
	if strings.Contains(result.Requests[1].Range, "secret") {
		t.Fatal("raw invalid range leaked")
	}
	result, err = ReadRecentRequests(context.Background(), storagePath(root), 50)
	if err != nil {
		t.Fatal(err)
	}
	if result.HasMore || len(result.Requests) != 4 {
		t.Fatalf("window: %+v", result)
	}
	if result.Requests[2].Outcome != "canceled" || len(result.Requests[2].Songs) != 0 {
		t.Fatalf("unknown: %+v", result.Requests[2])
	}
	if len(result.Requests[3].Songs) != 2 || result.Requests[3].Songs[0].Title != "共享歌曲" {
		t.Fatalf("songs: %+v", result.Requests[3])
	}
	// Query interpretation must not rewrite the existing outcome/statistics.
	var outcome string
	if err := u.db.QueryRow(`SELECT outcome FROM request_events WHERE resource_md5=?`, strings.Repeat("b", 32)).Scan(&outcome); err != nil || outcome != "completed" {
		t.Fatalf("mutated stored result: %s %v", outcome, err)
	}
}

func TestRecentMissingCorruptAndLimits(t *testing.T) {
	root := t.TempDir()
	a, err := ReadRecentRequests(context.Background(), storagePath(root), 50)
	if !os.IsNotExist(err) || len(a.Requests) != 0 {
		t.Fatalf("missing: %+v %v", a, err)
	}
	if _, err := os.Stat(filepath.Join(root, "storage.sqlite")); !os.IsNotExist(err) {
		t.Fatal("read created database")
	}
	b, _ := ReadRecentRequests(context.Background(), storagePath(t.TempDir()), 50)
	if a.StorageID == b.StorageID {
		t.Fatal("storage identity not changed")
	}
	for _, n := range []int{-1, 0, 501} {
		if _, err := ReadRecentRequests(context.Background(), storagePath(root), n); err == nil {
			t.Fatalf("accepted %d", n)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "storage.sqlite"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecentRequests(context.Background(), storagePath(root), 50); err == nil {
		t.Fatal("corrupt DB hidden")
	}
}

func TestRecentBoundedAssociations(t *testing.T) {
	root := t.TempDir()
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	for i := 1; i <= 30; i++ {
		if _, err = u.db.Exec(`INSERT INTO songs(song_id,name) VALUES (?,?);`, i, strings.Repeat("歌", 500)); err != nil {
			t.Fatal(err)
		}
		if _, err = u.db.Exec(`INSERT OR IGNORE INTO media(md5) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'); INSERT INTO song_media(song_id,md5) VALUES (?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`, i); err != nil {
			t.Fatal(err)
		}
	}
	if err = u.write([]usageEvent{{key: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRecentRequests(context.Background(), storagePath(root), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Requests) != 1 || len(r.Requests[0].Songs) != 20 || !r.Requests[0].MoreSongs || len([]rune(r.Requests[0].Songs[0].Title)) != 300 {
		t.Fatalf("unbounded: %+v", r)
	}
}

func TestRecentWindowOrderingAndOffline(t *testing.T) {
	root := t.TempDir()
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	events := []usageEvent{{key: strings.Repeat("a", 32), at: 1}}
	if err = u.write(events); err != nil {
		t.Fatal(err)
	}
	u.close()
	for _, limit := range []int{50, 500} {
		r, err := ReadRecentRequests(context.Background(), storagePath(root), limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Requests) != 1 || r.Requests[0].Resource != strings.Repeat("a", 32) || r.HasMore {
			t.Fatalf("limit %d: %+v", limit, r)
		}
	}
	// HasMore counts HTTP requests, including deterministic ties, not activity.
	u, err = fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	events = nil
	for i := 0; i < 50; i++ {
		events = append(events, usageEvent{key: strings.Repeat("b", 32), at: 1000})
	}
	if err = u.write(events); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRecentRequests(context.Background(), storagePath(root), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Requests) != 50 || !r.HasMore {
		t.Fatalf("HTTP page: %+v", r)
	}
	for i := 1; i < len(r.Requests); i++ {
		if r.Requests[i-1].ID <= r.Requests[i].ID {
			t.Fatal("unstable tie order")
		}
	}
	r, err = ReadRecentRequests(context.Background(), storagePath(root), 500)
	if err != nil || len(r.Requests) != 51 || r.HasMore {
		t.Fatalf("expanded HTTP page: %+v %v", r, err)
	}
	window, args := (RequestQuery{Limit: 500}).window(false)
	rows, err := u.db.Query(`EXPLAIN QUERY PLAN `+window, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "USING INDEX request_events_time") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("unbounded HTTP ordering: %s", plan)
	}
}
