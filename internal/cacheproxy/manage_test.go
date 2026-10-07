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
			if _, err := CacheDirectory(root); err == nil {
				t.Fatal("accepted symlink in cache path")
			}
		})
	}
}

func managementFixture(t *testing.T, n int) (string, []CacheEntry) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	u, err := openUsage(filepath.Join(root, "stepstash.sqlite"), slog.Default(), 0)
	if err != nil {
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
			{`INSERT INTO media VALUES (?, ?, 'private-source')`, []any{key, i + 1}},
			{`INSERT INTO songs(song_id,name) VALUES (?,?)`, []any{fmt.Sprint(i + 1), fmt.Sprintf("舞曲 %03d", i+1)}},
			{`INSERT INTO song_media VALUES (?,?)`, []any{fmt.Sprint(i + 1), key}},
		} {
			if _, err = u.db.Exec(statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	u.close()
	page, err := ReadCachePage(context.Background(), root, "", "size", 0)
	if err != nil {
		t.Fatal(err)
	}
	return root, page.Entries
}

func TestCachePageMetadataAndPagination(t *testing.T) {
	ctx := context.Background()
	root, _ := managementFixture(t, 53)
	page, err := ReadCachePage(ctx, root, "", "size", 0)
	if err != nil || len(page.Entries) != 50 || page.Total != 53 || page.Entries[0].Bytes != 53 {
		t.Fatalf("%+v %v", page, err)
	}
	if page.FileCount != 53 || page.TotalBytes != 53*54/2 {
		t.Fatalf("wrong whole-directory summary: %+v", page)
	}
	if !page.Entries[0].Songs[0].Current || !strings.Contains(page.Entries[0].State, "MD5") {
		t.Fatal(page.Entries[0])
	}
	page, err = ReadCachePage(ctx, root, "", "size", 50)
	if err != nil || len(page.Entries) != 3 || page.Entries[0].Bytes != 3 {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = ReadCachePage(ctx, root, "舞曲 007", "title", 0)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Songs[0].ID != "7" {
		t.Fatalf("%+v %v", page, err)
	}
	if page.FileCount != 53 || page.TotalBytes != 53*54/2 {
		t.Fatalf("search changed space usage: %+v", page)
	}
	if _, err = os.Stat(filepath.Join(root, "verification.sqlite")); !os.IsNotExist(err) {
		t.Fatal("listing created verification store")
	}
	if _, err = os.Stat(filepath.Join(root, ".lock")); !os.IsNotExist(err) {
		t.Fatal("listing started engine")
	}
	empty := filepath.Join(t.TempDir(), "absent")
	page, err = ReadCachePage(ctx, empty, "", "recent", 0)
	if err != nil || len(page.Entries) != 0 {
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
	u, err := openUsage(filepath.Join(root, "stepstash.sqlite"), slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.db.Exec(`INSERT INTO songs(song_id,name) VALUES ('2','共享歌'); INSERT INTO song_media VALUES ('2',?); INSERT INTO song_usage VALUES (2,2,2,200)`, e.Key)
	if err != nil {
		t.Fatal(err)
	}
	u.close()
	results, err := DeleteCacheOffline(ctx, root, []CacheSelection{{e.Key, e.Stamp}})
	if err != nil || results[0].Result != "changed" {
		t.Fatal(results, err)
	}
	page, err := ReadCachePage(ctx, root, "共享歌", "recent", 0)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].SongCount != 2 || page.Entries[0].LastRequest != 0 {
		t.Fatal(page, err)
	}
	e = page.Entries[0]
	if _, err = CacheFile(ctx, root, CacheSelection{e.Key, e.Stamp}); err != nil {
		t.Fatal(err)
	}
	results, err = DeleteCacheOffline(ctx, root, []CacheSelection{{e.Key, e.Stamp}})
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

func TestCacheDeletionProtectsPinsQueueChecksAndStore(t *testing.T) {
	root, entries := managementFixture(t, 1)
	e := entries[0]
	ctx := context.Background()
	cfg := fixtureConfig()
	cfg.StorageDir = root
	cfg.MaxCacheBytes = 1000
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
	s.SetQueueSongs([]int64{1})
	r, err = s.DeleteCache(ctx, selected)
	if err != nil || r[0].Result != "protected" {
		t.Fatal(r, err)
	}
	s.ResetQueueSongs(nil)
	release, err := verificationLocks.acquire(ctx, root+e.Key)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.DeleteCache(ctx, selected)
	release()
	if err != nil || r[0].Result != "protected" {
		t.Fatal(r, err)
	}
	if _, err = DeleteCacheOffline(ctx, root, selected); err == nil {
		t.Fatal("offline deletion bypassed store owner")
	}
	s.retentionMu.Lock()
	s.beginVideoRemovalLocked(e.Key)
	s.retentionMu.Unlock()
	queued := make(chan struct{})
	go func() { s.SetQueueSongs([]int64{1}); close(queued) }()
	select {
	case <-queued:
		t.Fatal("queue raced deletion")
	case <-time.After(20 * time.Millisecond):
	}
	s.finishVideoRemoval(e.Key, false)
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("queue blocked after reservation release")
	}
	s.ResetQueueSongs(nil)
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
	page, err := ReadCachePage(ctx, root, unknown, "recent", 0)
	if err != nil || page.Entries[0].Known {
		t.Fatal(page, err)
	}
	u := page.Entries[0]
	r, err := DeleteCacheOffline(ctx, root, []CacheSelection{{u.Key, u.Stamp}, {"../outside", "x"}})
	if err != nil || r[0].Result != "unknown" || r[1].Result != "failed" {
		t.Fatal(r, err)
	}
	if _, err = CacheFile(ctx, root, CacheSelection{"../outside", "x"}); err == nil {
		t.Fatal("accepted traversal")
	}
	if err = os.WriteFile(filepath.Join(root, "videos", e.Key+".mp4"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = DeleteCacheOffline(ctx, root, []CacheSelection{{e.Key, e.Stamp}})
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
	page, err := ReadCachePage(context.Background(), root, "", "recent", 0)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	r, err := DeleteCacheOffline(context.Background(), root, []CacheSelection{{e.Key, e.Stamp}})
	if err != nil || r[0].Result == "deleted" {
		t.Fatal(r, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatal("target damaged")
	}
}

func TestCachePageBoundsAssociationsAndSearchesHiddenSongs(t *testing.T) {
	root, entries := managementFixture(t, 1)
	u, err := openUsage(filepath.Join(root, "stepstash.sqlite"), slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 100; i < 125; i++ {
		id := fmt.Sprint(i)
		if _, err = u.db.Exec(`INSERT INTO songs(song_id,name) VALUES (?,?)`, id, "hidden-"+id+strings.Repeat("名", 350)); err != nil {
			t.Fatal(err)
		}
		if _, err = u.db.Exec(`INSERT INTO song_media VALUES (?,?)`, id, entries[0].Key); err != nil {
			t.Fatal(err)
		}
	}
	u.close()
	page, err := ReadCachePage(context.Background(), root, "hidden-124", "title", 0)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	e := page.Entries[0]
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
	if _, err := CacheDirectory(root); err == nil {
		t.Fatal("accepted directory redirect")
	}
}

func TestQueueUpdateCanCancelDeletionWaitWithoutApplyingPartialState(t *testing.T) {
	root, entries := managementFixture(t, 1)
	cfg := fixtureConfig()
	cfg.StorageDir = root
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetQueueSongs([]int64{2})
	s.retentionMu.Lock()
	reserved := s.beginVideoRemovalLocked(entries[0].Key)
	s.retentionMu.Unlock()
	if !reserved {
		t.Fatal("unable to reserve deletion")
	}
	defer s.finishVideoRemoval(entries[0].Key, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.UpdateQueueSongs(ctx, []int64{1}, true) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled queue update still waits for deletion")
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if !s.queueSongs["2"] || s.queueSongs["1"] {
		t.Fatal("canceled update changed reservations", s.queueSongs)
	}
}
