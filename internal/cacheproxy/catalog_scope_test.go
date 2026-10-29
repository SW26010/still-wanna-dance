package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
)

func TestCatalogMembersIncludeUnresolvedSongsAndKeepHistoricalTargets(t *testing.T) {
	bodies := map[int64]string{1: "historical video", 2: payload, 3: "third video", 4: "fourth video"}
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		for _, body := range bodies {
			if videoKey(body) == r.URL.Query().Get("e") {
				fmt.Fprint(w, body)
				return
			}
		}
		http.Error(w, "unknown resource", 404)
	})
	ctx := t.Context()
	if err := s.SyncCatalog(ctx, testCandidate("20261027000000", CatalogSong{ID: 1, MD5: videoKey(bodies[1])}, CatalogSong{ID: 2, MD5: videoKey(bodies[2])})); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncCatalog(ctx, Catalog{Revision: "20261028000000", Source: UdonCatalogURL, Songs: []CatalogSong{{ID: 2}, {ID: 3}, {ID: 4}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.videoFile(videoKey(bodies[2])), []byte(bodies[2]), 0600); err != nil {
		t.Fatal(err)
	}
	local, release, err := s.ReadLocalCatalog(ctx)
	defer release()
	if err != nil || len(local.Songs) != 3 || len(local.Files.References) != 3 || local.Files.Complete || local.Current["1"] {
		t.Fatal(local, err)
	}
	scan, err := s.NewDownloadFill(ctx, true, false).Run(local, func(_ context.Context, _ int64) (string, error) { t.Fatal("scan resolved a song"); return "", nil }, nil)
	if err != nil || scan.Total != 3 || scan.Checked != 3 || scan.Hits != 1 || scan.Unresolved != 2 || scan.Missing != 0 {
		t.Fatal(scan, err)
	}
	var mu sync.Mutex
	calls := map[int64]int{}
	prefetch := func(ctx context.Context, id int64) (string, error) {
		mu.Lock()
		calls[id]++
		mu.Unlock()
		return s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL(bodies[id]))
	}
	filled, err := s.NewDownloadFill(ctx, false, false).Run(local, prefetch, nil)
	if err != nil || filled.Total != 3 || filled.Checked != 3 || filled.Hits != 1 || filled.Downloaded != 2 || filled.Failed != 0 || calls[3] != 1 || calls[4] != 1 || calls[1] != 0 {
		t.Fatal(filled, calls, err)
	}
	for _, target := range []DownloadTarget{{SongID: "1"}, {Resource: videoKey(bodies[1])}} {
		old, done, err := s.ReadDownloadTarget(ctx, target)
		if err != nil || len(old.Songs) != 1 || old.Songs[0].ID != 1 || old.Current["1"] {
			done()
			t.Fatal(old, err)
		}
		result, err := s.NewDownloadFill(ctx, false, false).Run(old, prefetch, nil)
		done()
		if err != nil || result.Checked != 1 || result.Failed != 0 {
			t.Fatal(result, err)
		}
	}
	page, err := ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{})
	if err != nil || page.Total != 4 {
		t.Fatal(page, err)
	}
	var members int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM catalog_members WHERE song_id=1").Scan(&members); err != nil || members != 0 {
		t.Fatal(members, err)
	}
}
