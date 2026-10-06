package cacheproxy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCatalogConflictDiagnosticKeepsAcceptedData(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		s, _ := setup(t, nil)
		ctx := context.Background()
		name := "old"
		original := testCandidate("20261007000000", CatalogSong{ID: 42, MD5: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: &name})
		if err := s.SyncCatalog(ctx, original); err != nil {
			t.Fatal(err)
		}
		changed := original
		changed.Songs = append([]CatalogSong(nil), original.Songs...)
		if metadata {
			fresh := "new"
			changed.Songs[0].Name = &fresh
		} else {
			changed.Songs[0].MD5 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}
		err := s.SyncCatalog(ctx, changed)
		if !errors.Is(err, ErrCatalogConflict) {
			t.Fatal(err)
		}
		want := "MD5 改变 1"
		if metadata {
			want = "元数据或规范化表示变化"
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatal(err)
		}
		var key, storedName string
		if err := s.usage.db.QueryRow("SELECT md5,name FROM song_media JOIN songs USING(song_id) WHERE song_id=42").Scan(&key, &storedName); err != nil {
			t.Fatal(err)
		}
		if key != original.Songs[0].MD5 || storedName != "old" {
			t.Fatal("conflict changed accepted catalog", key, storedName)
		}
	}
}
