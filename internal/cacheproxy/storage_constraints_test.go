package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageV7Constraints(t *testing.T) {
	store, err := CreateStore(filepath.Join(t.TempDir(), "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.db.Exec(`INSERT INTO songs(id) VALUES(1); INSERT INTO media(md5) VALUES(?)`, key); err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"integer type":           `UPDATE media SET byte_size='not-a-number'`,
		"integer precision":      `UPDATE media SET byte_size=1.5`,
		"negative bytes":         `UPDATE media SET byte_size=-1`,
		"negative demand":        `INSERT INTO song_usage VALUES(1,-1,1,1)`,
		"negative score":         `INSERT INTO song_usage VALUES(1,1,-1,1)`,
		"score type":             `INSERT INTO song_usage VALUES(1,1,'not-a-number',1)`,
		"negative transfers":     `INSERT INTO song_playback VALUES(1,-1,1)`,
		"negative saved":         `UPDATE traffic_totals SET saved_bytes=-1`,
		"negative hits":          `UPDATE traffic_totals SET hits=-1`,
		"negative misses":        `UPDATE traffic_totals SET misses=-1`,
		"null catalog state key": `INSERT INTO catalog_state VALUES(NULL,'x','x','x',1)`,
		"null catalog check key": `INSERT INTO catalog_checks(catalog_key) VALUES(NULL)`,
		"invalid access MD5":     `INSERT INTO media_access VALUES('not-a-md5',1)`,
		"uppercase access MD5":   `INSERT INTO media_access VALUES('AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',1)`,
		"URL without song":       `INSERT INTO song_urls VALUES(999,'api','cf','url','` + key + `',1,1,1,0)`,
		"URL without media":      `INSERT INTO song_urls VALUES(1,'api','cf','url','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,1,1,0)`,
		"URL invalid MD5":        `INSERT INTO song_urls VALUES(1,'api','cf','url','zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz',1,1,1,0)`,
	} {
		t.Run(name, func(t *testing.T) {
			// Autocommit must enforce even the deferred foreign keys.
			if _, err := store.db.Exec(query); err == nil {
				t.Fatal("accepted invalid data", query)
			}
		})
	}
	var strict int
	if err := store.db.QueryRow(`SELECT count(*) FROM pragma_table_list WHERE schema='main' AND name NOT LIKE 'sqlite_%' AND strict=1`).Scan(&strict); err != nil || strict != 14 {
		t.Fatal("all business tables must enforce types", strict, err)
	}
	// URL evidence, song and resource may arrive in that order in one transaction.
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO song_urls VALUES(2,'api','cf','url','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,1,1,0);
INSERT INTO songs(id) VALUES(2); INSERT INTO media(md5) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("complete URL transaction rejected", err)
	}
}

func TestRequestHistoryIdentityAndConstraints(t *testing.T) {
	store, err := CreateStore(filepath.Join(t.TempDir(), "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Resolution failures can identify a song absent from the catalog. Unknown
	// identities are NULL on disk, but the existing JSON contract uses "".
	if err := store.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	events := []usageEvent{
		{songID: "9223372036854775807", method: "GET", status: 502, at: 1},
		{method: "GET", status: 400, at: 2},
		{key: strings.Repeat("a", 32), method: "GET", at: 3},
	}
	if err := store.usage.write(events); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := store.db.QueryRow(`SELECT typeof(song_id)='integer' AND resource_md5 IS NULL FROM request_events WHERE requested_at=1`).Scan(&valid); err != nil || !valid {
		t.Fatal("typed identity lost", valid, err)
	}
	if err := store.db.QueryRow(`SELECT song_id IS NULL AND resource_md5 IS NULL FROM request_events WHERE requested_at=2`).Scan(&valid); err != nil || !valid {
		t.Fatal("unknown identity must be NULL", valid, err)
	}
	for name, assignments := range map[string]string{
		"song text": `song_id='bad'`, "song zero": `song_id=0`, "song fraction": `song_id=1.5`,
		"empty resource": `resource_md5=''`, "invalid resource": `resource_md5='bad'`,
		"negative file": `file_bytes=-1`, "negative transfer": `transferred_bytes=-1`,
		"negative elapsed": `elapsed_ms=-1`, "negative response": `response_bytes=-1`,
		"negative first body": `first_body_ns=-1`, "invalid blocking": `first_body_blocking=2`,
		"partial sources":    `download_bytes=0`,
		"negative sources":   `download_bytes=-1,cache_bytes=0,local_bytes=0`,
		"fractional sources": `download_bytes=0.5,cache_bytes=0,local_bytes=0`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.db.Exec("UPDATE request_events SET " + assignments); err == nil {
				t.Fatal("accepted invalid history", assignments)
			}
		})
	}
	page, err := ReadRequestHistory(context.Background(), store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 3 {
		t.Fatal(page, err)
	}
	if page.Requests[2].SongID != "9223372036854775807" || page.Requests[2].Resource != "" || page.Requests[1].SongID != "" || page.Requests[1].Resource != "" {
		t.Fatal("JSON identity contract changed", page)
	}
	// Access evidence remains independent of the resource catalog too.
	if _, err := store.db.Exec(`INSERT INTO media_access VALUES(?,1)`, strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
}

func TestPrefetchDoesNotPersistRequestHistory(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, payload)
				}
			})
			_, err := s.PrefetchSong(t.Context(), "42", videoURL(payload))
			if (err == nil) != (status == 200) {
				t.Fatalf("prefetch status %d: %v", status, err)
			}
			s.usage.flush()
			for _, table := range []string{"request_events", "song_usage", "song_playback", "media_access"} {
				var count int
				if err := s.usage.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("preparation persisted request facts", table, count, err)
				}
			}
			if stats := s.TrafficStats(); stats.Requests != 0 || stats.SavedBytes != 0 || stats.PlaybackTransfers != 0 {
				t.Fatal(stats)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			page, err := ReadRecentRequests(t.Context(), cfg.Store.path, 50)
			if err != nil || len(page.Requests) != 0 {
				t.Fatal(page, err)
			}
		})
	}
}
