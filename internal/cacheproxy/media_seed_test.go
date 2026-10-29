package cacheproxy

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mediaseed "still-wanna-dance/data/media-seed"
)

func TestMediaOnlyImportUsesActualBudgetAndMappingScore(t *testing.T) {
	s, cfg := setup(t, nil)
	orphan, mapped := "orphan-A", "mapped-B"
	a, b := fmt.Sprintf("%x", md5.Sum([]byte(orphan))), fmt.Sprintf("%x", md5.Sum([]byte(mapped)))
	if err := s.applyMediaSeed(t.Context(), seedJSON(t, "20261029000000", []string{a, b}, mediaseed.Mapping{ID: 1, MD5: b})); err != nil {
		t.Fatal(err)
	}
	if err := s.usage.write([]usageEvent{{songID: "1", at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.KnownMedia(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{orphan, mapped} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("%d.mp4", i))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if receipt, err := s.ImportVideo(t.Context(), path, allowed); err != nil || receipt == nil {
			t.Fatal(receipt, err)
		}
	}
	setRetentionLimit(t, s, int64(len(mapped)))
	s.trimCache()
	expectRetained(t, cfg.videoFile(a), false)
	expectRetained(t, cfg.videoFile(b), true)
}

func seedJSON(t *testing.T, version string, media []string, mappings ...mediaseed.Mapping) string {
	t.Helper()
	resources := make([]mediaseed.Media, 0, len(media))
	for _, key := range media {
		resources = append(resources, mediaseed.Media{MD5: key})
	}
	body, err := json.Marshal(mediaseed.Snapshot{Version: version, Media: resources, SongMedia: mappings})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestMediaSeedFillsOnlyAndHasIndependentVersion(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := t.Context()
	a, b, c := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	seed := seedJSON(t, "20261029000000", []string{a, b}, mediaseed.Mapping{ID: 1, MD5: a})
	if err := s.applyMediaSeed(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := s.db.QueryRow("SELECT revision FROM catalog_state WHERE catalog_key='media_seed'").Scan(&version); err != nil || version != "20261029000000" {
		t.Fatal(version, err)
	}
	status, err := s.CatalogStatus(ctx)
	if err != nil || status.Revision != "" || !status.CheckedAt.IsZero() {
		t.Fatal("seed became a song catalog", status, err)
	}
	var members int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM catalog_members").Scan(&members); err != nil || members != 0 {
		t.Fatal(members, err)
	}
	known, err := s.KnownMedia(ctx)
	if err != nil || len(known) != 2 || !known[a] || !known[b] {
		t.Fatal(known, err)
	}
	// A repeated seed is a no-op, including when a real API already corrected it.
	at := time.Now()
	if err := s.ObserveSongURL(ctx, SongURL{SongID: 1, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", URL: "https://play.udon.dance/files/1/1-new.mp4?e=" + b + "&s=24", QueryStartedAt: at, ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyMediaSeed(ctx, seed); err != nil {
		t.Fatal(err)
	}
	newer := seedJSON(t, "20261030000000", []string{a, b, c}, mediaseed.Mapping{ID: 1, MD5: a}, mediaseed.Mapping{ID: 2, MD5: c})
	if err := s.applyMediaSeed(ctx, newer); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", b)
	assertSongResource(t, s, "2", c)
	var size int64
	if err := s.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", b).Scan(&size); err != nil || size != 24 {
		t.Fatal("seed erased known size", size, err)
	}
	if err := s.applyMediaSeed(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT revision FROM catalog_state WHERE catalog_key='media_seed'").Scan(&version); err != nil || version != "20261030000000" {
		t.Fatal("old program regressed seed marker", version, err)
	}
	if err := s.applyMediaSeed(ctx, strings.Replace(newer, `"id":2`, `"id":3`, 1)); err == nil {
		t.Fatal("same-version seed conflict accepted")
	}
}

func TestMediaSeedValidationAndAtomicRollback(t *testing.T) {
	a := strings.Repeat("a", 32)
	for _, encoded := range []string{
		`{}`, seedJSON(t, "bad", []string{a}), seedJSON(t, "20261029000000", []string{"bad"}),
		seedJSON(t, "20261029000000", []string{a, a}),
		seedJSON(t, "20261029000000", []string{a}, mediaseed.Mapping{ID: 1, MD5: strings.Repeat("b", 32)}),
		seedJSON(t, "20261029000000", []string{a}, mediaseed.Mapping{ID: 1, MD5: a}, mediaseed.Mapping{ID: 1, MD5: a}),
	} {
		s, _ := setup(t, nil)
		if err := s.applyMediaSeed(t.Context(), encoded); err == nil {
			t.Fatal("accepted invalid seed", encoded)
		}
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM media").Scan(&count); err != nil || count != 0 {
			t.Fatal(count, err)
		}
	}
	s, _ := setup(t, nil)
	if _, err := s.db.Exec("CREATE TRIGGER fail_seed BEFORE INSERT ON song_media BEGIN SELECT RAISE(ABORT,'seed failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.applyMediaSeed(t.Context(), seedJSON(t, "20261029000000", []string{a}, mediaseed.Mapping{ID: 1, MD5: a})); err == nil {
		t.Fatal("failure committed")
	}
	for _, table := range []string{"songs", "media", "song_media", "catalog_state"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}

func TestMediaSeedSizesOnlyFillHoles(t *testing.T) {
	s, _ := setup(t, nil)
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if _, err := s.db.Exec("INSERT INTO media(md5,byte_size) VALUES (?,24),(?,NULL)", a, b); err != nil {
		t.Fatal(err)
	}
	size := int64(48)
	seed := mediaseed.Snapshot{Version: "20261029000000", Media: []mediaseed.Media{{MD5: a, ByteSize: &size}, {MD5: b, ByteSize: &size}}}
	body, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyMediaSeed(t.Context(), string(body)); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{a: 24, b: 48} {
		var got int64
		if err := s.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", key).Scan(&got); err != nil || got != want {
			t.Fatal(got, want, err)
		}
	}
}

func TestBundledMediaSeedIntegrityAndReopen(t *testing.T) {
	s, cfg := setup(t, nil)
	if err := s.ApplyBundledMediaSeed(t.Context()); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"media": 10446, "song_media": 10471, "songs": 10471, "catalog_members": 0, "song_urls": 0, "song_usage": 0} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, want, err)
		}
	}
	var nullSize int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM media WHERE byte_size IS NULL").Scan(&nullSize); err != nil || nullSize != 29 {
		t.Fatal(nullSize, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopenFixtureStore(t, &cfg)
	if err := cfg.Store.ApplyBundledMediaSeed(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := cfg.Store.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("broken foreign key")
	}
}
