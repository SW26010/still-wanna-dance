package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineSongPolicyRespectsStorageLockAndVersion(t *testing.T) {
	t.Run("locked", func(t *testing.T) {
		s, cfg := setup(t, nil)
		if err := SetSongPolicyOffline(t.Context(), cfg.StorageDir, 42, SongFavorite); err == nil {
			t.Fatal("offline write bypassed the engine's storage lock")
		}
		policy, err := readSongPolicy(t.Context(), s.usage.db, 42)
		if err != nil || policy != SongNormal {
			t.Fatal(policy, err)
		}
	})
	t.Run("v2", func(t *testing.T) {
		root := t.TempDir()
		original, err := os.ReadFile(filepath.Join("testdata", "storage-v2.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(storagePath(root), original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := SetSongPolicyOffline(t.Context(), root, 42, SongFavorite); err == nil {
			t.Fatal("offline write accepted schema v2")
		}
		after, err := os.ReadFile(storagePath(root))
		if err != nil || !bytes.Equal(original, after) {
			t.Fatal("offline write changed old database", err)
		}
	})
}

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

func TestSongPolicyNotifiesQueueOnlyAfterCommit(t *testing.T) {
	s, _ := setup(t, nil)
	changed := s.QueuePreparationChanged()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.SetSongPolicy(ctx, 42, SongBlocked); err == nil {
		t.Fatal("canceled preference write succeeded")
	}
	select {
	case <-changed:
		t.Fatal("failed preference write notified the queue")
	default:
	}
	for _, policy := range []SongPolicy{SongBlocked, SongNormal} {
		changed = s.QueuePreparationChanged()
		if err := s.SetSongPolicy(t.Context(), 42, policy); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
		default:
			t.Fatal("committed preference did not notify the queue", policy)
		}
		policies, err := s.SongPolicies(t.Context())
		if err != nil || (policies[42] == SongBlocked) != (policy == SongBlocked) {
			t.Fatal("notification preceded committed state", policies, err)
		}
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
