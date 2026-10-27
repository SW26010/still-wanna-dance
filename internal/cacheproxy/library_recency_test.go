package cacheproxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestLibraryResourcePageBatchOrderingAndDetails(t *testing.T) {
	root, _ := managementFixture(t, 270) // crosses the metadata batch boundary
	u, err := fixtureUsage(t, filepath.Join(root, "storage.sqlite"), slog.Default(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	if _, err := u.db.Exec(`UPDATE songs SET name='same';
 UPDATE songs SET name='ÄBC' WHERE song_id='257';
 UPDATE songs SET name='' WHERE song_id='258'`); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 256, 257, 270} {
		key := fmt.Sprintf("%032x", i)
		if _, err := u.db.Exec(`INSERT INTO request_events(resource_md5,requested_at,host,method,range_header,cache_result,outcome,file_bytes,transferred_bytes,elapsed_ms,status) VALUES (?,100,'local','GET','','HIT','completed',1,1,1,200)`, key); err != nil {
			t.Fatal(err)
		}
	}
	// Completed requests can be newer than the persisted GET-start summary.
	for _, event := range []struct {
		i, at int
	}{{257, 200}, {259, 300}} {
		key := fmt.Sprintf("%032x", event.i)
		if _, err := u.db.Exec(`INSERT INTO request_events
 (resource_md5,requested_at,host,method,range_header,cache_result,outcome,
 file_bytes,transferred_bytes,elapsed_ms,status)
 VALUES (?,?,'local','GET','','HIT','completed',1,1,1,200)`, key, event.at); err != nil {
			t.Fatal(err)
		}
	}
	// A catalog row without a file must not be listed; an unknown file must be.
	if err := os.Remove(filepath.Join(root, "videos", fmt.Sprintf("%032x.mp4", 2))); err != nil {
		t.Fatal(err)
	}
	unknown := strings.Repeat("f", 32)
	if err := os.WriteFile(filepath.Join(root, "videos", unknown+".mp4"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// Include a size mismatch and a size tie.
	if err := os.WriteFile(filepath.Join(root, "videos", fmt.Sprintf("%032x.mp4", 270)), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := cacheReadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var all []CacheEntry
	for i := 1; i <= 270; i++ {
		if i == 2 {
			continue
		}
		e, err := readCacheEntry(ctx, fixtureLibrary(root), fmt.Sprintf("%032x", i), db)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, e)
	}
	e, err := readCacheEntry(ctx, fixtureLibrary(root), unknown, db)
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, e)
	for _, order := range []string{"size", "recent", "title", ""} {
		t.Run(order, func(t *testing.T) {
			want := append([]CacheEntry(nil), all...)
			title := func(e CacheEntry) string {
				if len(e.Songs) != 0 {
					return e.Songs[0].Title
				}
				return ""
			}
			sort.Slice(want, func(i, j int) bool {
				a, b := want[i], want[j]
				switch {
				case order == "size" && a.Bytes != b.Bytes:
					return a.Bytes > b.Bytes
				case order == "recent" && a.LastRequest != b.LastRequest:
					return a.LastRequest > b.LastRequest
				case order == "title" && title(a) != title(b):
					return title(a) < title(b)
				default:
					return a.Key < b.Key
				}
			})
			for _, offset := range []int{0, 50, 250, 270, 1000} {
				page, err := ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: order, Offset: offset})
				if err != nil {
					t.Fatal(err)
				}
				start, end := min(offset, len(want)), min(offset+50, len(want))
				entries := []CacheEntry{}
				for _, key := range page.Keys {
					entries = append(entries, *page.Resources[key].Local)
				}
				if page.Total != len(want) || !reflect.DeepEqual(entries, want[start:end]) {
					t.Fatalf("offset %d: page differs from individually validated entries", offset)
				}
			}
		})
	}
	for _, query := range []string{"äbc", "same", unknown} {
		page, err := ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: query, Sort: "title", Offset: 0})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if query == "same" {
			want = 267
		}
		if page.Total != want {
			t.Fatalf("query %q: got %d matches, want %d", query, page.Total, want)
		}
	}
}

func TestLibraryResourcePageWithoutDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 32)
	if err := os.WriteFile(filepath.Join(root, "videos", key+".mp4"), []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"size", "recent", "title"} {
		page, err := ReadLibraryPage(context.Background(), fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: order, Offset: 0})
		if err == nil || page.Total != 0 || len(page.Keys) != 0 {
			t.Fatal("missing database must be reported", page, err)
		}
	}
}
