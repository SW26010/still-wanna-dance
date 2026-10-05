package cacheproxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCatalogProtocolSampleAndAtomicRoundTrip(t *testing.T) {
	c, err := ParseCatalog([]byte(catalogSample), "authoritative")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := setup(t, nil)
	ctx := context.Background()
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	var name, artist, group, tags, urls, shader string
	var volume, start, end float64
	var flip, width int
	var aya sql.NullString
	err = s.usage.db.QueryRow("SELECT name,artist,group_name,volume,start,end,flip,double_width,aya_id,tags_json,original_urls_json,shader_motion_json FROM songs WHERE song_id=90000").Scan(&name, &artist, &group, &volume, &start, &end, &flip, &width, &aya, &tags, &urls, &shader)
	if err != nil {
		t.Fatal(err)
	}
	if name != "测试" || artist != "测试" || group != "Hide" || volume != 1 || start != 0 || end != 8 || flip != 1 || width != 0 || aya.Valid || tags != "[]" || shader != "[]" || !strings.Contains(urls, "bilibili.com") {
		t.Fatal(name, artist, group, volume, start, end, flip, width, aya, tags, urls, shader)
	}
	// Same normalized data despite object formatting is a duplicate, not a write.
	var generic any
	if err := json.Unmarshal([]byte(catalogSample), &generic); err != nil {
		t.Fatal(err)
	}
	formatted, _ := json.MarshalIndent(generic, "", "  ")
	duplicate, err := ParseCatalog(formatted, "authoritative")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("CREATE TRIGGER forbid_song_update BEFORE UPDATE ON songs BEGIN SELECT RAISE(ABORT,'duplicate wrote'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncCatalog(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
}

func testCandidate(revision string, songs ...CatalogSong) Catalog {
	return Catalog{Source: "test", Revision: revision, Songs: songs}
}

func TestCatalogAyaIDNumberStoredLosslesslyAsNullableText(t *testing.T) {
	for _, value := range []string{"null", "0", "42", "9007199254740993", "18446744073709551616", "-12", "1.2300", "1e+400"} {
		t.Run(value, func(t *testing.T) {
			body := strings.Replace(catalogSample, `"ayaId":null`, `"ayaId":`+value, 1)
			c, err := ParseCatalog([]byte(body), "test")
			if err != nil {
				t.Fatal(err)
			}
			s, _ := setup(t, nil)
			if err := s.SyncCatalog(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			var got sql.NullString
			var storageType string
			if err := s.usage.db.QueryRow("SELECT aya_id,typeof(aya_id) FROM songs WHERE song_id=90000").Scan(&got, &storageType); err != nil {
				t.Fatal(err)
			}
			if value == "null" {
				if got.Valid || storageType != "null" || c.Songs[0].AyaID != nil {
					t.Fatal(got, storageType, string(c.Songs[0].AyaID))
				}
			} else if !got.Valid || got.String != value || storageType != "text" || string(c.Songs[0].AyaID) != value {
				t.Fatal(got, storageType, string(c.Songs[0].AyaID))
			}
		})
	}
}

func TestCatalogUpdatesPreserveUsageResourcesAndAbsentSongs(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	name := "old"
	c := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: a, Name: &name}, CatalogSong{ID: 2, MD5: a})
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.recordVideo(ctx, video{key: a, size: 100, path: "/known"}); err != nil {
		t.Fatal(err)
	}
	if err := s.usage.write([]usageEvent{{id: "1", at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	name = "new"
	c = testCandidate("20261002000000", CatalogSong{ID: 1, MD5: b, Name: &name})
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", b)
	assertSongResource(t, s, "2", a)
	var count, size int
	var got, path string
	if err := s.usage.db.QueryRow("SELECT name FROM songs WHERE song_id=1").Scan(&got); err != nil || got != "new" {
		t.Fatal(got, err)
	}
	if err := s.usage.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := s.usage.db.QueryRow("SELECT byte_size,source_path FROM media WHERE md5=?", a).Scan(&size, &path); err != nil || size != 100 || path != "/known" {
		t.Fatal(size, path, err)
	}
	if err := s.usage.db.QueryRow("SELECT count(*) FROM song_media WHERE song_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// No historical mapping or redundant authoritative column remains.
	if err := s.usage.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('video_versions','current_videos','song_videos','resource_usage')").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	for _, query := range []string{"INSERT INTO song_media VALUES (999,'" + a + "')", "INSERT INTO media(md5) VALUES (NULL)", "UPDATE songs SET flip=2 WHERE song_id=1", "UPDATE songs SET tags_json='{}' WHERE song_id=1"} {
		if _, err := s.usage.db.Exec(query); err == nil {
			t.Fatal("constraint not enforced", query)
		}
	}
}

func TestCatalogRollbackAndWatermark(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	first := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: a})
	if err := s.SyncCatalog(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("CREATE TRIGGER reject_second BEFORE INSERT ON songs WHEN NEW.song_id=2 BEGIN SELECT RAISE(ABORT,'injected failure'); END"); err != nil {
		t.Fatal(err)
	}
	next := testCandidate("20261002000000", CatalogSong{ID: 1, MD5: b}, CatalogSong{ID: 2, MD5: b})
	if err := s.SyncCatalog(ctx, next); err == nil {
		t.Fatal("injected transaction committed")
	}
	assertSongResource(t, s, "1", a)
	var revision string
	var n int
	if err := s.usage.db.QueryRow("SELECT revision FROM catalog_state").Scan(&revision); err != nil || revision != first.Revision {
		t.Fatal(revision, err)
	}
	if err := s.usage.db.QueryRow("SELECT count(*) FROM media WHERE md5=?", b).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := s.usage.db.Exec("DROP TRIGGER reject_second"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncCatalog(ctx, next); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Catalog{first, testCandidate(next.Revision, CatalogSong{ID: 1, MD5: a})} {
		if err := s.SyncCatalog(ctx, bad); err == nil {
			t.Fatal("stale/conflicting candidate accepted")
		}
	}
	assertSongResource(t, s, "1", b)
}

func TestCatalogConcurrentOrderingAndPlaybackFill(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if err := s.recordVideo(ctx, video{key: a, size: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordSongVideo(ctx, "1", video{key: a}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordVideo(ctx, video{key: b, size: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordSongVideo(ctx, "1", video{key: b}); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", a)
	old := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: a})
	newer := testCandidate("20261002000000", CatalogSong{ID: 1, MD5: b})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.SyncCatalog(ctx, old) }()
	go func() {
		defer wg.Done()
		if err := s.SyncCatalog(ctx, newer); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	assertSongResource(t, s, "1", b)
	// Simulate an older fetch finishing after the new version has committed.
	if err := s.SyncCatalog(ctx, old); err == nil {
		t.Fatal("late older candidate accepted")
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := s.recordSongVideo(ctx, "1", video{key: a}); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := s.SyncCatalog(ctx, newer); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	assertSongResource(t, s, "1", b)
}

func TestCatalogTimesAcrossMD5SourcesIgnoreLegacyUdonWatermark(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	name := "old"
	first := Catalog{Revision: "20260215004959", Source: "https://wanna.kiva.moe/api/wannaInfo", Songs: []CatalogSong{{ID: 1, MD5: a, Name: &name}}}
	if err := s.SyncCatalog(ctx, first); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.usage.db.QueryRow("SELECT count(*) FROM catalog_state").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// Existing obsolete rows must never gate, advance, or be rewritten by MD5 sync.
	if _, err := s.usage.db.Exec("INSERT INTO catalog_state VALUES ('song_names','20990101000000','legacy','udon',1)"); err != nil {
		t.Fatal(err)
	}
	name = "new"
	next := Catalog{Revision: "20261004235822", Source: "https://x.kiva.moe/api/v2/wanna/songs", Songs: []CatalogSong{{ID: 1, MD5: b, Name: &name}}}
	if err := s.SyncCatalog(ctx, next); err != nil {
		t.Fatal("obsolete Udon watermark blocked update", err)
	}
	assertSongResource(t, s, "1", b)
	if err := s.SyncCatalog(ctx, first); err == nil {
		t.Fatal("old mirror accepted")
	}
	if _, err := s.usage.db.Exec("CREATE TRIGGER forbid_update BEFORE UPDATE ON songs BEGIN SELECT RAISE(ABORT,'duplicate wrote'); END"); err != nil {
		t.Fatal(err)
	}
	next.Source = first.Source
	if err := s.SyncCatalog(ctx, next); err != nil {
		t.Fatal("same-version mirror rewrote data", err)
	}
	next.Songs[0].MD5 = a
	if err := s.SyncCatalog(ctx, next); err == nil {
		t.Fatal("same-version conflict accepted")
	}
	var revision string
	if err := s.usage.db.QueryRow("SELECT revision FROM catalog_state WHERE catalog_key='song_names'").Scan(&revision); err != nil || revision != "20990101000000" {
		t.Fatal(revision, err)
	}
}

func TestOldStoreRejectedWithoutAnyFileChanges(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "stepstash.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("PRAGMA user_version=2; CREATE TABLE songs(song_id TEXT PRIMARY KEY,title TEXT); INSERT INTO songs VALUES ('1','preserve')")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(root, "videos", strings.Repeat("a", 32)+".mp4")
	partial := filepath.Join(root, "tmp", "preserve.part")
	writeTestFile(t, videoPath, "video")
	writeTestFile(t, partial, "partial")
	before := map[string][]byte{}
	for _, p := range []string{dbPath, videoPath, partial} {
		before[p], err = os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := fixtureConfig()
	cfg.StorageDir = root
	if service, err := New(cfg); err == nil {
		service.Close()
		t.Fatal("opened old store")
	}
	if _, err := DeleteCacheOffline(context.Background(), root, []CacheSelection{{Key: strings.Repeat("a", 32)}}); err == nil {
		t.Fatal("old store deletion accepted")
	}
	if _, err := ReadRecentRequests(context.Background(), root, 10); err == nil {
		t.Fatal("old store recent read accepted")
	}
	for p, want := range before {
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("old data modified", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "verification.sqlite")); !os.IsNotExist(err) {
		t.Fatal("created verification database", err)
	}
}
