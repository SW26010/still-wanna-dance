package cacheproxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheDirectoryRejectsSymlinkComponents(t *testing.T) {
	for _, component := range []string{"videos", "root", "ancestor"} {
		t.Run(component, func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "target")
			for _, path := range []string{filepath.Join(target, "videos"), filepath.Join(target, "cache", "videos")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(base, "link")
			root := link
			switch component {
			case "videos":
				root, link = base, filepath.Join(base, "videos")
			case "ancestor":
				root = filepath.Join(link, "cache")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skip("symlink privilege unavailable:", err)
			}
			if _, err := CacheDirectory(fixtureLibrary(root).VideoDir); err == nil {
				t.Fatal("accepted symlink in cache path")
			}
		})
	}
}

func managementFixture(t *testing.T, n int) (string, []CacheEntry) {
	t.Helper()
	root := t.TempDir()
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%032x", i+1)
		if err := os.WriteFile(filepath.Join(root, "videos", key+".mp4"), []byte(strings.Repeat("x", i+1)), 0600); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO media(md5,byte_size) VALUES (?, ?)`, []any{key, i + 1}},
			{`INSERT INTO songs(id,name) VALUES (?,?)`, []any{fmt.Sprint(i + 1), fmt.Sprintf("舞曲 %03d", i+1)}},
			{`INSERT INTO song_media VALUES (?,?)`, []any{fmt.Sprint(i + 1), key}},
		} {
			if _, err = u.db.Exec(statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	u.close()
	page, err := ReadLibraryPage(context.Background(), fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "size", Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	var entries []CacheEntry
	for _, key := range page.Keys {
		entries = append(entries, *page.Resources[key].Local)
	}
	return root, entries
}

func TestLibraryResourcePageMetadataAndPagination(t *testing.T) {
	ctx := context.Background()
	root, _ := managementFixture(t, 53)
	page, err := ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "size", Offset: 0})
	if err != nil || len(page.Keys) != 50 || page.Total != 53 || (*page.Resources[page.Keys[0]].Local).Bytes != 53 {
		t.Fatalf("%+v %v", page, err)
	}
	if page.FileCount != 53 || page.TotalBytes != 53*54/2 {
		t.Fatalf("wrong whole-directory summary: %+v", page)
	}
	if !strings.Contains((*page.Resources[page.Keys[0]].Local).State, "MD5") {
		t.Fatal((*page.Resources[page.Keys[0]].Local))
	}
	page, err = ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "size", Offset: 50})
	if err != nil || len(page.Keys) != 3 || (*page.Resources[page.Keys[0]].Local).Bytes != 3 {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "舞曲 007", Sort: "title", Offset: 0})
	if err != nil || len(page.Keys) != 1 || (*page.Resources[page.Keys[0]].Local).Songs[0].ID != "7" {
		t.Fatalf("%+v %v", page, err)
	}
	if page.FileCount != 53 || page.TotalBytes != 53*54/2 {
		t.Fatalf("search changed space usage: %+v", page)
	}
	if _, err = os.Stat(filepath.Join(root, "verification.sqlite")); !os.IsNotExist(err) {
		t.Fatal("listing created verification store")
	}
	for _, path := range []string{filepath.Join(root, "tmp"), filepath.Join(root, "videos", ".swd.lock"), filepath.Join(root, "videos", ".swd-tmp")} {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("listing created video-engine resources", path, err)
		}
	}
	empty := filepath.Join(t.TempDir(), "absent")
	page, err = ReadLibraryPage(ctx, fixtureLibrary(empty), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "recent", Offset: 0})
	if err == nil || len(page.Keys) != 0 {
		t.Fatal(page, err)
	}
	if _, err = os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("listing created root")
	}
}

func TestCacheDeletionSharedMetadataAndStaleConfirmation(t *testing.T) {
	ctx := context.Background()
	root, entries := managementFixture(t, 1)
	e := entries[0]
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.db.Exec(`INSERT INTO songs(id,name) VALUES ('2','共享歌'); INSERT INTO song_media VALUES ('2',?); INSERT INTO song_usage VALUES (2,2,2,200)`, e.Key)
	if err != nil {
		t.Fatal(err)
	}
	u.close()
	results, err := DeleteCacheOffline(ctx, fixtureLibrary(root), []CacheSelection{{e.Key, e.Stamp}})
	if err != nil || results[0].Result != "changed" {
		t.Fatal(results, err)
	}
	page, err := ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "共享歌", Sort: "recent", Offset: 0})
	if err != nil || len(page.Keys) != 1 || (*page.Resources[page.Keys[0]].Local).SongCount != 2 || (*page.Resources[page.Keys[0]].Local).LastRequest != 0 {
		t.Fatal(page, err)
	}
	e = (*page.Resources[page.Keys[0]].Local)
	if _, err = CacheFile(ctx, fixtureLibrary(root), CacheSelection{e.Key, e.Stamp}); err != nil {
		t.Fatal(err)
	}
	results, err = DeleteCacheOffline(ctx, fixtureLibrary(root), []CacheSelection{{e.Key, e.Stamp}})
	if err != nil || results[0].Result != "deleted" {
		t.Fatal(results, err)
	}
	if _, err = os.Stat(filepath.Join(root, "videos", e.Key+".mp4")); !os.IsNotExist(err) {
		t.Fatal("file retained")
	}
	db, err := cacheReadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, want := range map[string]int{"songs": 2, "song_media": 2, "media": 1, "song_usage": 1} {
		var n int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatal(table, n, err)
		}
	}
}

func TestCacheDeletionProtectsPinsUsageChecksAndStore(t *testing.T) {
	root, entries := managementFixture(t, 1)
	e := entries[0]
	ctx := context.Background()
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, root, false)
	cfg.MaxCacheBytes = 1000
	cfg.CacheUnlimited = false
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	selected := []CacheSelection{{e.Key, e.Stamp}}
	s.pinVideo(video{key: e.Key})
	r, err := s.DeleteCache(ctx, selected)
	if err != nil || r[0].Result != "protected" {
		t.Fatal(r, err)
	}
	s.releaseVideo(video{key: e.Key})
	protectTestVideo(s, e.Key)
	r, err = s.DeleteCache(ctx, selected)
	if err != nil || r[0].Result != "protected" {
		t.Fatal(r, err)
	}
	clearTestProtection(s)
	release, err := verificationLocks.acquire(ctx, pathIdentity(fixtureLibrary(root).VideoDir)+e.Key)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.DeleteCache(ctx, selected)
	release()
	if err != nil || r[0].Result != "protected" {
		t.Fatal(r, err)
	}
	if _, err = DeleteCacheOffline(ctx, fixtureLibrary(root), selected); err == nil {
		t.Fatal("offline deletion bypassed store owner")
	}
	s.retentionMu.Lock()
	s.beginVideoRemovalLocked(e.Key)
	s.retentionMu.Unlock()
	queued := make(chan struct{})
	go func() { s.pinVideo(video{key: e.Key}); close(queued) }()
	select {
	case <-queued:
		t.Fatal("pin raced deletion")
	case <-time.After(20 * time.Millisecond):
	}
	s.finishVideoRemoval(e.Key, false)
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("pin blocked after reservation release")
	}
	s.releaseVideo(video{key: e.Key})
	r, err = s.DeleteCache(ctx, selected)
	if err != nil || r[0].Result != "deleted" {
		t.Fatal(r, err)
	}
	s.retentionMu.Lock()
	remaining := s.retainedBytes
	s.retentionMu.Unlock()
	if remaining != 0 {
		t.Fatal("manual removal left retained bytes", remaining)
	}
}

func TestCacheManagementRejectsUnknownChangedAndPaths(t *testing.T) {
	root, entries := managementFixture(t, 1)
	e := entries[0]
	ctx := context.Background()
	unknown := strings.Repeat("f", 32)
	if err := os.WriteFile(filepath.Join(root, "videos", unknown+".mp4"), []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: unknown, Sort: "recent"})
	if err != nil || (*page.Resources[page.Keys[0]].Local).Known {
		t.Fatal(page, err)
	}
	u := (*page.Resources[page.Keys[0]].Local)
	r, err := DeleteCacheOffline(ctx, fixtureLibrary(root), []CacheSelection{{u.Key, u.Stamp}, {"../outside", "x"}})
	if err != nil || r[0].Result != "unknown" || r[1].Result != "failed" {
		t.Fatal(r, err)
	}
	if _, err = CacheFile(ctx, fixtureLibrary(root), CacheSelection{"../outside", "x"}); err == nil {
		t.Fatal("accepted traversal")
	}
	if err = os.WriteFile(filepath.Join(root, "videos", e.Key+".mp4"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = DeleteCacheOffline(ctx, fixtureLibrary(root), []CacheSelection{{e.Key, e.Stamp}})
	if err != nil || r[0].Result != "changed" {
		t.Fatal(r, err)
	}
}

func TestCacheManagementRejectsSymlinks(t *testing.T) {
	root, entries := managementFixture(t, 1)
	e := entries[0]
	path := filepath.Join(root, "videos", e.Key+".mp4")
	target := filepath.Join(t.TempDir(), "keep.mp4")
	os.WriteFile(target, []byte("keep"), 0600)
	os.Remove(path)
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlink privilege unavailable:", err)
	}
	page, err := ReadLibraryPage(context.Background(), fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "recent", Offset: 0})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	r, err := DeleteCacheOffline(context.Background(), fixtureLibrary(root), []CacheSelection{{e.Key, e.Stamp}})
	if err != nil || r[0].Result == "deleted" {
		t.Fatal(r, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatal("target damaged")
	}
}

func TestLibraryResourcePageBoundsAssociationsAndSearchesHiddenSongs(t *testing.T) {
	root, entries := managementFixture(t, 1)
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 100; i < 125; i++ {
		id := fmt.Sprint(i)
		if _, err = u.db.Exec(`INSERT INTO songs(id,name) VALUES (?,?)`, id, "hidden-"+id+strings.Repeat("名", 350)); err != nil {
			t.Fatal(err)
		}
		if _, err = u.db.Exec(`INSERT INTO song_media VALUES (?,?)`, id, entries[0].Key); err != nil {
			t.Fatal(err)
		}
	}
	u.close()
	page, err := ReadLibraryPage(context.Background(), fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "hidden-124", Sort: "title", Offset: 0})
	if err != nil || len(page.Keys) != 1 {
		t.Fatal(page, err)
	}
	e := (*page.Resources[page.Keys[0]].Local)
	if e.SongCount != 26 || len(e.Songs) != 20 {
		t.Fatal(e)
	}
	for _, s := range e.Songs {
		if len([]rune(s.Title)) > 300 {
			t.Fatal("unbounded title")
		}
	}
}

func TestCacheManagementRejectsDirectoryLinks(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "videos")); err != nil {
		t.Skip("symlink privilege unavailable:", err)
	}
	if _, err := CacheDirectory(fixtureLibrary(root).VideoDir); err == nil {
		t.Fatal("accepted directory redirect")
	}
}
