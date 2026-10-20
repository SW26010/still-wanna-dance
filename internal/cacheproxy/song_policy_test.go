package cacheproxy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSongPoliciesPersistWithoutDownloadOrCatalogMutation(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	if err := s.SetSongPolicy(ctx, 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := syncTestCatalog(s, ctx, map[string]string{"42": strings.Repeat("a", 32)}); err != nil {
		t.Fatal(err)
	}
	before, err := s.CatalogStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSongPolicy(ctx, 42, SongBlocked); err != nil {
		t.Fatal(err)
	}
	after, _ := s.CatalogStatus(ctx)
	if before != after {
		t.Fatal("preference changed catalog state")
	}
	if err := s.CheckSongAllowed(ctx, 42); !errors.Is(err, ErrSongBlocked) {
		t.Fatal(err)
	}
	s.mu.Lock()
	if len(s.demands) != 0 || len(s.flights) != 0 || len(s.useBlocks) != 0 {
		t.Error("marking a song created download or usage")
	}
	s.mu.Unlock()
	s.Close()
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CheckSongAllowed(ctx, 42); !errors.Is(err, ErrSongBlocked) {
		t.Fatal("lost preference after restart", err)
	}
	if err := reopened.SetSongPolicy(ctx, 42, SongNormal); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.usage.db.QueryRow("SELECT COUNT(*) FROM song_preferences").Scan(&count); err != nil || count != 0 {
		t.Fatal("normal should have no preference row", count, err)
	}
}

func TestResourcePoliciesFollowSharedCurrentAssociations(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if err := syncTestCatalog(s, ctx, map[string]string{"1": a, "2": a}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		first, second SongPolicy
		want          resourcePolicy
	}{
		{SongBlocked, SongNormal, resourcePolicy{}},
		{SongBlocked, SongFavorite, resourcePolicy{favorite: true}},
		{SongBlocked, SongBlocked, resourcePolicy{blocked: true}},
		{SongNormal, SongNormal, resourcePolicy{}},
	} {
		if err := s.SetSongPolicy(ctx, 1, tc.first); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSongPolicy(ctx, 2, tc.second); err != nil {
			t.Fatal(err)
		}
		got, err := s.resourcePolicies(ctx)
		if err != nil || got[a] != tc.want || got[b] != (resourcePolicy{}) {
			t.Fatalf("%s/%s: %v, %v", tc.first, tc.second, got, err)
		}
	}
	if err := s.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := syncTestCatalog(s, ctx, map[string]string{"1": b, "2": a}); err != nil {
		t.Fatal(err)
	}
	got, err := s.resourcePolicies(ctx)
	if err != nil || got[a].favorite || !got[b].favorite {
		t.Fatal(got, err)
	}
}

func TestSongPolicyValidationAndTransactionRollback(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	for _, tc := range []struct {
		id     int64
		policy SongPolicy
	}{{0, SongBlocked}, {-1, SongFavorite}, {1, "unknown"}} {
		if err := s.SetSongPolicy(ctx, tc.id, tc.policy); err == nil {
			t.Fatal("accepted invalid preference", tc)
		}
	}
	if _, err := s.usage.db.Exec(`CREATE TRIGGER reject_preference BEFORE INSERT ON song_preferences BEGIN SELECT RAISE(ABORT,'fail'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSongPolicy(ctx, 123, SongFavorite); err == nil {
		t.Fatal("expected failure")
	}
	var count int
	if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM songs WHERE song_id=123").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial placeholder write", count, err)
	}
}
