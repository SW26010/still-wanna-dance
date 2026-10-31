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
	s = New(Options{SongCandidates: func(context.Context) []int64 { s.UpdatedAt(); return slices.Clone(pending) }})
	defer s.Close()
	for i, want := range []int64{73, 91, 73, 42} {
		if i == 3 {
			pending = nil
		}
		if got := s.SelectSong(context.Background(), 42); got != want {
			t.Fatal(i, got, want)
		}
	}
	pending = []int64{91}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.SelectSong(ctx, 42) != 0 {
		t.Fatal("canceled selection")
	}
	if s.SelectSong(context.Background(), 42) != 91 {
		t.Fatal("cancellation advanced traversal")
	}
}

func TestSampleReuseExpiresWithoutExtendingOnRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Options{})
		ctx := t.Context()
		if s.SelectSong(ctx, 42) != 42 {
			t.Fatal("fresh sample lost")
		}
		time.Sleep(5 * time.Minute)
		if s.SelectSong(ctx, 0) != 42 {
			t.Fatal("recent sample not reused")
		}
		time.Sleep(5 * time.Minute)
		if s.SelectSong(ctx, 0) != 0 {
			t.Fatal("expired sample reused")
		}
		s.Close()
		if s.SelectSong(ctx, 73) != 0 {
			t.Fatal("selection after shutdown")
		}
	})
}

func TestMetadataTraversalSharesProgressWithBatchExclusions(t *testing.T) {
	s := New(Options{SongCandidates: func(context.Context) []int64 { return []int64{1, 2, 3} }})
	defer s.Close()
	ctx := t.Context()
	if s.SelectSong(ctx, 42) != 1 || s.SelectSongExcept(ctx, 42, map[int64]bool{2: true}) != 3 {
		t.Fatal("batch did not reuse traversal or exclude its existing sample")
	}
	if s.SelectSong(ctx, 42) != 2 {
		t.Fatal("exclusion incorrectly advanced the skipped song")
	}
	if s.SelectSongExcept(ctx, 42, map[int64]bool{1: true, 2: true, 3: true, 42: true}) != 0 {
		t.Fatal("batch repeated a completed or in-flight song")
	}
}
