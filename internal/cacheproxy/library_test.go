package cacheproxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestLibrarySharedResourcesAndScope(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	shared, missing, orphan, hidden := videoKey("shared"), videoKey("missing"), videoKey("orphan"), videoKey("hidden")
	for id, key := range map[string]string{"1": shared, "2": shared, "3": missing, "4": orphan, "5": hidden} {
		seedPrioritySong(t, s, id, key)
	}
	if _, err := s.usage.db.Exec(`DELETE FROM song_media WHERE song_id IN (4,5); UPDATE songs SET name='Song',artist='Singer',dancer='Dancer',group_name='Group' WHERE song_id=1; INSERT INTO song_preferences VALUES (1,'favorite'); INSERT INTO song_usage VALUES (1,1,8,?)`, time.Now().Add(-24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cfg.videoFile(shared), "shared")
	writeTestFile(t, cfg.videoFile(orphan), "orphan")
	// Neither a partial spool nor an invalid file becomes a complete video.
	writeTestFile(t, filepath.Join(cfg.VideoDir, "unfinished.part"), "ignored")
	page, err := ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources"})
	if err != nil || page.Total != 3 || len(page.Resources) != 3 || page.FileCount != 2 || page.TotalBytes != 12 {
		t.Fatal(page, err)
	}
	r := page.Resources[shared]
	if r.SongCount != 2 || r.Local == nil || r.Local.Stamp == "" || *r.Size != 6 || r.Duration != nil {
		t.Fatal(r)
	}
	if page.Resources[missing].Local != nil || page.Resources[missing].Duration != nil || page.Resources[hidden] != nil {
		t.Fatal(page)
	}
	if _, err := s.usage.db.Exec("INSERT INTO media_access(md5,last_requested_at) VALUES (?,?), (?,?)", shared, 100, orphan, 200); err != nil {
		t.Fatal(err)
	}
	recent, err := ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources", Sort: "recent"})
	if err != nil || len(recent.Keys) != 3 || recent.Keys[0] != orphan || recent.Keys[1] != shared || recent.Keys[2] != missing || recent.Resources[orphan].Local.LastRequest != 200 {
		t.Fatal(recent, err)
	}
	songs, err := ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{Search: "Singer"})
	if err != nil || songs.Total != 1 || songs.Songs[0].Artist != "Singer" || songs.Songs[0].Dancer != "Dancer" || songs.Songs[0].Group != "Group" || songs.Songs[0].Policy != SongFavorite {
		t.Fatal(songs, err)
	}
	if songs.Resources[shared].Local.Stamp != r.Local.Stamp {
		t.Fatal("shared file has inconsistent deletion identity")
	}
	page, err = ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources", Filter: "missing"})
	if err != nil || page.Total != 1 || page.Keys[0] != missing {
		t.Fatal(page, err)
	}
	page, err = ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources", Resource: orphan})
	if err != nil || page.Total != 1 || page.Resources[orphan].SongCount != 0 {
		t.Fatal(page, err)
	}
	removed, err := s.DeleteCache(ctx, []CacheSelection{{Key: orphan, Stamp: page.Resources[orphan].Local.Stamp}})
	if err != nil || len(removed) != 1 || removed[0].Result != "deleted" {
		t.Fatal(removed, err)
	}
	page, err = ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources", Resource: orphan})
	if err != nil || page.Total != 0 || page.FileCount != 1 || page.TotalBytes != 6 {
		t.Fatal(page, err)
	}
	var count int
	if err := s.usage.db.QueryRow("SELECT count(*) FROM media WHERE md5=?", orphan).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := os.Remove(cfg.videoFile(shared)); err != nil {
		t.Fatal(err)
	}
	page, err = ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources", Resource: shared})
	if err != nil || page.Total != 1 || page.Resources[shared].Local != nil {
		t.Fatal(page, err)
	}
	if len(s.demands) != 0 || len(s.flights) != 0 {
		t.Fatal("read-only library started work")
	}
}

func TestLibraryPagesScoresAndExactIdentity(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	for id := int64(9000001); id <= 9000121; id++ {
		if _, err := s.usage.db.Exec(`INSERT INTO songs(song_id,name) VALUES (?,?)`, id, fmt.Sprintf("Song %d", id)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.usage.db.Exec(`INSERT INTO song_usage VALUES (?,1,?,?)`, id, float64(id-9000000), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.usage.db.Exec(`UPDATE song_usage SET last_demand_at=? WHERE song_id=9000121; INSERT INTO song_preferences VALUES (9000120,'blocked')`, now-int64(365*24*time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	priorities, err := s.songPriorities(ctx, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	lastScore := 1e9
	for offset := 0; offset < 121; offset += 50 {
		page, err := readLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{Sort: "score", Offset: offset}, now)
		if err != nil || page.Total != 121 || len(page.Songs) > 50 || len(page.Resources) != 0 {
			t.Fatal(page, err)
		}
		for _, song := range page.Songs {
			if seen[song.ID] || song.Score > lastScore {
				t.Fatal(song)
			}
			seen[song.ID], lastScore = true, song.Score
		}
		if offset == 0 && (page.Songs[0].ID != "9000120" || page.Songs[0].Policy != SongBlocked || page.Songs[0].Score != priorities[9000120]) {
			t.Fatal(page)
		}
	}
	if len(seen) != 121 {
		t.Fatal(len(seen))
	}
	page, err := ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{SongID: "9000001"})
	if err != nil || page.Total != 1 || page.Songs[0].ID != "9000001" || page.Songs[0].Resource != "" {
		t.Fatal(page, err)
	}
	page, err = ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{SongID: "9999999"})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestLibraryMissingStorageRemainsReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	page, err := ReadLibraryPage(context.Background(), fixtureLibrary(root), LibraryQuery{})
	if err == nil || page.Total != 0 {
		t.Fatal("missing database must be reported", page, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("reader created storage", err)
	}
}

func TestLibraryDemandScoresPrecedeInitialScoresBeforePagination(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	for id := int64(9000001); id <= 9000051; id++ {
		if _, err := s.usage.db.Exec(`INSERT INTO songs(song_id,name) VALUES (?,'Z real')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.usage.db.Exec(`INSERT INTO song_usage(song_id,demand_count,demand_score,last_demand_at) VALUES (?,1,?,?)`, id, float64(id-9000001)/1000, now); err != nil {
			t.Fatal(err)
		}
	}
	// Every real score is below the priors, including one real score of zero.
	if _, err := s.usage.db.Exec(`INSERT INTO songs(song_id,name) VALUES (3119,'A initial'),(2167,'B initial'),(9999999,'C unknown')`); err != nil {
		t.Fatal(err)
	}
	first, err := readLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{Sort: "score"}, now)
	if err != nil || first.Total != 54 || len(first.Songs) != 50 {
		t.Fatal(first, err)
	}
	for i, song := range first.Songs {
		if !song.HasDemand || song.ID != fmt.Sprint(9000051-i) {
			t.Fatal("real scores must fill the first page in descending order", song)
		}
	}
	second, err := readLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{Sort: "score", Offset: 50}, now)
	if err != nil || len(second.Songs) != 4 {
		t.Fatal(second, err)
	}
	for i, id := range []string{"9000001", "3119", "2167", "9999999"} {
		if second.Songs[i].ID != id || second.Songs[i].HasDemand != (i == 0) {
			t.Fatal("source grouping must precede pagination and preserve descending scores within each group", second.Songs)
		}
	}
	if second.Songs[0].Score != 0 || second.Songs[1].Score != 0.5 {
		t.Fatal("UI grouping must not rescale or replace actual scores", second.Songs)
	}
	for _, order := range []struct{ sort, first string }{{"title", "3119"}, {"id", "9999999"}} {
		page, err := readLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{Sort: order.sort}, now)
		if err != nil || page.Songs[0].ID != order.first {
			t.Fatal("source grouping leaked into another sort", order, page, err)
		}
	}
}

func TestLibrarySortDirectionsAcrossPages(t *testing.T) {
	s, cfg := setup(t, nil)
	now := time.Now().UnixMilli()
	for id := 1; id <= 63; id++ {
		key := fmt.Sprintf("%032x", id)
		seedPrioritySong(t, s, fmt.Sprint(9000000+id), key)
		if _, err := s.usage.db.Exec("UPDATE songs SET name=? WHERE song_id=?", fmt.Sprintf("Song %03d", 64-id), 9000000+id); err != nil {
			t.Fatal(err)
		}
		if id <= 61 {
			if _, err := s.usage.db.Exec("INSERT INTO song_usage VALUES (?,1,?,?)", 9000000+id, id, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.usage.db.Exec("UPDATE media SET byte_size=? WHERE md5=?", id*100, key); err != nil {
				t.Fatal(err)
			}
		} else if _, err := s.usage.db.Exec("UPDATE media SET byte_size=NULL WHERE md5=?", key); err != nil {
			t.Fatal(err)
		}
	}
	for _, direction := range []string{"ascending", "descending"} {
		for _, field := range []string{"title", "score", "size", "resource"} {
			q := LibraryQuery{Sort: field, Direction: direction}
			if field == "size" || field == "resource" {
				q.View = "resources"
			}
			var got []int
			for q.Offset = 0; q.Offset < 63; q.Offset += 50 {
				page, err := readLibraryPage(t.Context(), cfg.libraryPaths(), q, now)
				if err != nil || page.Total != 63 {
					t.Fatal(page, err)
				}
				if q.View == "resources" {
					for _, key := range page.Keys {
						id, err := strconv.ParseInt(key, 16, 64)
						if err != nil {
							t.Fatal(err)
						}
						got = append(got, int(id))
					}
				} else {
					for _, song := range page.Songs {
						id, _ := strconv.Atoi(song.ID)
						got = append(got, id-9000000)
					}
				}
			}
			var want []int
			n := 63
			if field == "score" || field == "size" {
				n = 61
			}
			for id := 1; id <= n; id++ {
				next := id
				if direction == "descending" {
					next = n + 1 - id
				}
				if field == "title" {
					next = n + 1 - next
				}
				want = append(want, next)
			}
			if field == "score" {
				want = append(want, 63, 62)
			} // Same initial score: ID descending.
			if field == "size" {
				want = append(want, 62, 63)
			} // Unknown sizes stay last in both directions.
			if !slices.Equal(got, want) {
				t.Fatalf("%s %s: got %v want %v", field, direction, got, want)
			}
		}
	}
}
