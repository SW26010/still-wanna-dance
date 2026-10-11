package cacheproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This binary was created for schema v2. Never regenerate it to make a changed
// schema pass: add a new version and retain the prior release's fixture.
func TestStorageV2Golden(t *testing.T) {
	b, err := os.ReadFile("testdata/storage-v2.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), storageDatabaseName)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, writable := range []bool{false, true, true, false} {
		db, err := openStorageDatabase(path, writable)
		if err != nil {
			t.Fatal(err)
		}
		var millis, nanos, duration int64
		var absent bool
		if err := db.QueryRow(`SELECT last_demand_at FROM song_usage WHERE song_id=42`).Scan(&millis); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT query_started_at FROM song_urls WHERE song_id=42`).Scan(&nanos); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT first_body_ns FROM playback_latency WHERE cache_result='HIT'`).Scan(&duration); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT artist IS NULL AND flip=0 AND volume=0 FROM songs WHERE song_id=42`).Scan(&absent); err != nil {
			t.Fatal(err)
		}
		var digest string
		if err := db.QueryRow(`SELECT digest FROM catalog_state WHERE catalog_key='songs'`).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		name, volume, flip := "Golden", 0.0, false
		wantDigest, err := catalogDigestV1([]CatalogSong{{ID: 42, MD5: "0123456789abcdef0123456789abcdef", Name: &name, Volume: &volume, Flip: &flip}})
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
