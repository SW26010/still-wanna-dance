package catalogsync

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestMetadataTraversalAndCancellation(t *testing.T) {
	pending := []int64{73, 91}
	var s *Sync
	s = New(Options{SongCandidates: func(context.Context) []int64 { s.CatalogError(); return slices.Clone(pending) }})
	defer s.Close()
	members := map[int64]bool{42: true, 73: true, 91: true}
	for i, want := range []int64{73, 91, 73, 42} {
		if i == 3 {
			pending = nil
		}
		if got := s.SelectCatalogSong(context.Background(), 42, members); got != want {
			t.Fatal(i, got, want)
		}
	}
	pending = []int64{91}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.SelectCatalogSong(ctx, 42, members) != 0 {
		t.Fatal("canceled selection")
	}
	if s.SelectCatalogSong(context.Background(), 42, members) != 91 {
		t.Fatal("cancellation advanced traversal")
	}
}

func TestSampleReuseExpiresWithoutExtendingOnRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Options{})
		ctx := t.Context()
		members := map[int64]bool{42: true}
		if s.SelectCatalogSong(ctx, 42, members) != 42 {
			t.Fatal("fresh sample lost")
		}
		if s.SelectCatalogSong(ctx, 0, map[int64]bool{73: true}) != 0 {
			t.Fatal("removed catalog member reused")
		}
		time.Sleep(5 * time.Minute)
		if s.SelectCatalogSong(ctx, 73, members) != 42 {
			t.Fatal("recent sample not reused")
		}
		time.Sleep(5 * time.Minute)
		if s.SelectCatalogSong(ctx, 0, members) != 0 {
			t.Fatal("expired sample reused")
		}
		s.Close()
		if s.SelectCatalogSong(ctx, 73, map[int64]bool{73: true}) != 0 {
			t.Fatal("selection after shutdown")
		}
	})
}

func TestRememberSampleDoesNotAdvanceMetadataTraversal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pending := []int64{73, 91}
		s := New(Options{SongCandidates: func(context.Context) []int64 { return slices.Clone(pending) }})
		ctx := t.Context()
		members := map[int64]bool{42: true, 73: true, 91: true}
		s.RememberSample(ctx, 42)
		if s.SelectCatalogSong(ctx, 0, members) != 73 || s.SelectCatalogSong(ctx, 0, members) != 91 {
			t.Fatal("remembering a sample advanced metadata traversal")
		}
		pending = nil
		if s.SelectCatalogSong(ctx, 0, members) != 42 {
			t.Fatal("sample was not reused after pending metadata was exhausted")
		}
		time.Sleep(10 * time.Minute)
		if s.SelectCatalogSong(ctx, 0, members) != 0 {
			t.Fatal("remembered sample outlived its existing validity period")
		}
	})
}

func TestMetadataTraversalSharesProgressWithBatchExclusions(t *testing.T) {
	s := New(Options{SongCandidates: func(context.Context) []int64 { return []int64{1, 2, 3} }})
	defer s.Close()
	ctx := t.Context()
	members := map[int64]bool{1: true, 2: true, 42: true}
	if s.SelectCatalogSong(ctx, 42, members) != 1 || s.SelectSongExcept(ctx, 42, map[int64]bool{2: true}) != 3 {
		t.Fatal("batch did not reuse traversal or exclude its existing sample")
	}
	if s.SelectCatalogSong(ctx, 42, members) != 2 {
		t.Fatal("exclusion incorrectly advanced the skipped song")
	}
	if s.SelectCatalogSong(ctx, 42, members) != 1 || s.SelectSongExcept(ctx, 42, nil) != 2 {
		t.Fatal("restarting catalog traversal should preserve attempts outside the catalog")
	}
	if s.SelectSongExcept(ctx, 42, map[int64]bool{1: true, 2: true, 3: true, 42: true}) != 0 {
		t.Fatal("batch repeated a completed or in-flight song")
	}
}
