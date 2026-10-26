package cacheproxy

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSongPageAndFavoriteLifecycleDoNotDownload(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	key := videoKey(payload)
	seedPrioritySong(t, s, "42", key)
	if err := s.SetSongPolicy(ctx, 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	h := &SongUsageImport{}
	h.Observe(42, time.Now().Add(-time.Hour).UnixMilli())
	if err := s.ImportSongUsage(ctx, h, true); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 60; n++ {
		if _, err := s.usage.db.Exec("INSERT INTO songs(song_id,name) VALUES (?,?) ON CONFLICT DO UPDATE SET name=excluded.name", n, fmt.Sprintf("歌曲 %d", n)); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, cfg.videoFile(key), payload)
	page, err := ReadSongPage(ctx, cfg.libraryPaths(), "", SongNormal, 0)
	if err != nil || page.Total != 59 || len(page.Entries) != 50 {
		t.Fatal(page, err)
	}
	page, err = ReadSongPage(ctx, cfg.libraryPaths(), "", SongNormal, 50)
	if err != nil || len(page.Entries) != 9 {
		t.Fatal(page, err)
	}
	page, err = ReadSongPage(ctx, cfg.libraryPaths(), "42", SongFavorite, 0)
	if err != nil || page.Total != 1 || !page.Entries[0].Saved {
		t.Fatal(page, err)
	}
	if err := os.Remove(cfg.videoFile(key)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopenFixtureStore(t, &cfg)
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := syncTestCatalog(reopened, ctx, map[string]string{"42": videoKey("new-version")}); err != nil {
		t.Fatal(err)
	}
	page, err = ReadSongPage(ctx, cfg.libraryPaths(), "42", SongFavorite, 0)
	if err != nil || page.Total != 1 || page.Entries[0].Saved {
		t.Fatal(page, err)
	}
	reopened.mu.Lock()
	empty := len(reopened.demands) == 0 && len(reopened.useBlocks) == 0 && len(reopened.flights) == 0
	reopened.mu.Unlock()
	if !empty {
		t.Fatal("restart/missing file/mapping change created favorite demand")
	}
}
