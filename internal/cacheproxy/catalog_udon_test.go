package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCatalogNewerBackupWarningNeedsObservedUdon(t *testing.T) {
	s, _ := setup(t, nil)
	var logs bytes.Buffer
	s.Store.log = slog.New(slog.NewJSONHandler(&logs, nil))
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	backup := Catalog{Revision: "20261003000000", Source: WannaCatalogURL, ObservedAt: at, Songs: []CatalogSong{{ID: 1}}}
	if err := s.SyncCatalog(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "catalog_backup_newer_than_udon") {
		t.Fatal("absence is not evidence of an older Udon")
	}
	udon := backup
	udon.Source = UdonCatalogURL
	udon.Revision = "20261002000000"
	udon.ObservedAt = at.Add(time.Second)
	if err := s.SyncCatalog(t.Context(), udon); !errors.Is(err, ErrCatalogOlder) {
		t.Fatal(err)
	}
	for _, want := range []string{"catalog_backup_newer_than_udon", WannaCatalogURL, backup.Revision, udon.Revision, "observed_at", "udon_observed_at"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatal("missing warning detail", want, logs.String())
		}
	}
}

func TestUdonAndBackupShareMetadataDigest(t *testing.T) {
	const udon = `{"time":"20261003235746","groups":{"contents":[{"groupName":"Hide","major":"","songInfos":[{"id":90000,"danceid":90000,"name":"测试","artist":"测试","dancer":"测试","playerCount":1,"volume":0.35,"start":0,"end":8,"flip":true,"double_width":false,"skip_random":1,"disable_public":1,"rpe":100,"tag":null,"shader_motion":""}]}]}}`
	backup := strings.Replace(catalogSample, `"volume":1.0`, `"volume":0.3499999940395355`, 1)
	a, err := ParseCatalog([]byte(udon), UdonCatalogURL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseCatalog([]byte(backup), KivaCatalogURL)
	if err != nil {
		t.Fatal(err)
	}
	_, ad, err := normalizeCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	_, bd, err := normalizeCatalog(b)
	if err != nil || ad != bd {
		t.Fatal(ad, bd, err)
	}
	if a.Songs[0].MD5 != "" || b.Songs[0].MD5 == "" {
		t.Fatal("resource evidence lost", a, b)
	}
	b.Songs[0].MD5 = strings.Repeat("a", 32)
	_, bd, err = normalizeCatalog(b)
	if err != nil || ad != bd {
		t.Fatal("MD5 changed metadata identity", bd, err)
	}
	for _, bad := range []string{
		strings.Replace(udon, `"id":90000`, `"id":0`, 1),
		strings.Replace(udon, `"skip_random":1`, `"skip_random":2`, 1),
		strings.Replace(udon, `"songInfos":`, `"missing":`, 1),
		strings.Replace(udon, `"shader_motion":""`, `"shader_motion":{}`, 1),
	} {
		if _, err := ParseCatalog([]byte(bad), UdonCatalogURL); err == nil {
			t.Fatal("accepted invalid Udon", bad)
		}
	}
}

func TestUdonAuthorityAndOlderBackupFill(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	name, canonical := "backup", "udon"
	backup := Catalog{Revision: "20261002000000", Source: KivaCatalogURL, Songs: []CatalogSong{{ID: 1, MD5: a, Name: &name}}}
	if err := s.SyncCatalog(ctx, backup); err != nil {
		t.Fatal(err)
	}
	udon := Catalog{Revision: backup.Revision, Source: UdonCatalogURL, Songs: []CatalogSong{{ID: 1, Name: &canonical}, {ID: 2}}}
	if err := s.SyncCatalog(ctx, udon); err != nil {
		t.Fatal(err)
	}
	backup.Songs[0].MD5 = b
	if err := s.SyncCatalog(ctx, backup); err != nil {
		t.Fatal("same-version backup cannot replace Udon", err)
	}
	old := Catalog{Revision: "20261001000000", Source: WannaCatalogURL, Songs: []CatalogSong{{ID: 1, MD5: b}, {ID: 2, MD5: b}, {ID: 3, MD5: a}}}
	if err := s.SyncCatalog(ctx, old); !errors.Is(err, ErrCatalogOlder) {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", a)
	assertSongResource(t, s, "2", b)
	assertSongResource(t, s, "3", a)
	var title, source string
	var members int
	if err := s.db.QueryRow("SELECT name FROM songs WHERE id=1").Scan(&title); err != nil || title != canonical {
		t.Fatal(title, err)
	}
	if err := s.db.QueryRow("SELECT source FROM catalog_state WHERE catalog_key='songs'").Scan(&source); err != nil || source != UdonCatalogURL {
		t.Fatal(source, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM catalog_members WHERE song_id IN (1,2)").Scan(&members); err != nil || members != 2 {
		t.Fatal(members, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM catalog_members WHERE song_id=3").Scan(&members); err != nil || members != 0 {
		t.Fatal(members, err)
	}
	// A newer backup legitimately advances song metadata, still only filling MD5 holes.
	backup.Revision = "20261003000000"
	if err := s.SyncCatalog(ctx, backup); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", a)
	if err := s.db.QueryRow("SELECT name FROM songs WHERE id=1").Scan(&title); err != nil || title != name {
		t.Fatal(title, err)
	}
}

func TestSongURLOrdersAcrossNodesAndCannotBeUndoneByCatalog(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute)
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	c := Catalog{Revision: "20261001000000", Source: KivaCatalogURL, Songs: []CatalogSong{{ID: 42, MD5: a}}}
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	o := SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", URL: "https://play.udon.dance/files/1/42-new.mp4?e=" + b + "&s=24", QueryStartedAt: at, ObservedAt: at}
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	late := o
	late.Node = "nya"
	late.QueryStartedAt = at.Add(-time.Second)
	late.ObservedAt = at.Add(time.Second)
	late.URL = strings.Replace(o.URL, b, a, 1)
	if err := s.ObserveSongURL(ctx, late); err != nil {
		t.Fatal(err)
	}
	c.Revision = "20261002000000"
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.recordSongVideo(ctx, "42", video{key: a}); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "42", b)
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM song_urls").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
