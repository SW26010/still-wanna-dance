package cacheproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This binary was created for schema v8. Never regenerate it to make a changed
// schema pass: add a new version and retain the prior release's fixture.
func TestStorageV8Golden(t *testing.T) {
	b, err := os.ReadFile("testdata/storage-v8.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), storageDatabaseName)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, writable := range []bool{false, true, true, false} {
		db, err := openFixtureDatabase(path, writable)
		if err != nil {
			t.Fatal(err)
		}
		var millis, nanos, duration int64
		var sources ResponseSources
		if err := db.QueryRow("SELECT download_bytes,cache_bytes,local_bytes FROM request_events WHERE event_id=1").Scan(&sources.DownloadBytes, &sources.CacheBytes, &sources.LocalBytes); err != nil || sources != (ResponseSources{DownloadBytes: 64, CacheBytes: 36}) {
			t.Fatal("golden source bytes changed", sources, err)
		}
		var absent bool
		if err := db.QueryRow(`SELECT last_demand_at FROM song_usage WHERE song_id=42`).Scan(&millis); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT query_started_at FROM song_urls WHERE song_id=42`).Scan(&nanos); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT first_body_ns FROM request_events WHERE event_id=1`).Scan(&duration); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT artist IS NULL AND flip=0 AND volume=0 FROM songs WHERE id=42`).Scan(&absent); err != nil {
			t.Fatal(err)
		}
		var resource string
		if err := db.QueryRow("SELECT resource_md5 FROM request_events WHERE event_id=1").Scan(&resource); err != nil || resource != "0123456789abcdef0123456789abcdef" {
			t.Fatal("golden request resource changed", resource, err)
		}
		var digest string
		var policy string
		if err := db.QueryRow("SELECT policy FROM song_preferences WHERE song_id=42").Scan(&policy); err != nil || policy != "favorite" {
			t.Fatal("golden preference changed", policy, err)
		}
		if err := db.QueryRow(`SELECT digest FROM catalog_state WHERE catalog_key='songs'`).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		name, volume, flip := "Golden", 0.0, false
		wantDigest, err := catalogDigestV2([]CatalogSong{{ID: 42, MD5: "0123456789abcdef0123456789abcdef", Name: &name, Volume: &volume, Flip: &flip}})
		if err != nil || digest != wantDigest {
			t.Fatal("golden digest changed", digest, wantDigest, err)
		}
		db.Close()
		if millis != 1791716220123 || nanos != 1791716220123456789 || duration != 123456789 || !absent {
			t.Fatal(millis, nanos, duration, absent)
		}
		if time.UnixMilli(millis).UTC().Format(time.RFC3339Nano) != "2026-10-11T10:57:00.123Z" {
			t.Fatal("millisecond contract changed")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, after) {
		t.Fatal("reopening rewrote golden", err)
	}
}

func TestStorageOldGoldensRejectedUnchanged(t *testing.T) {
	for _, version := range []string{"2", "3", "4", "5", "6", "7"} {
		t.Run(version, func(t *testing.T) {
			b, err := os.ReadFile("testdata/storage-v" + version + ".sqlite")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), storageDatabaseName)
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			for _, writable := range []bool{false, true} {
				db, err := openFixtureDatabase(path, writable)
				if db != nil {
					db.Close()
				}
				if err == nil {
					t.Fatal("old version must require a new storage directory")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(b, after) {
				t.Fatal("old fixture changed", err)
			}
		})
	}
}
