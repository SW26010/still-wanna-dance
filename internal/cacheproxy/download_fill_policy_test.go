package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

// Exercise the real fill and SQLite adapter with local-hit snapshots. No
// network or physical video I/O is needed to expose per-resource policy reads.
func fillPolicyFixture(t testing.TB, resources, preferences int) (*Server, LocalCatalog) {
	t.Helper()
	store, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if preferences > 0 {
		if _, err := store.db.Exec(`BEGIN;
WITH RECURSIVE ids(id) AS (SELECT 1 UNION ALL SELECT id+1 FROM ids WHERE id < ?)
INSERT INTO songs(id) SELECT id FROM ids;
INSERT INTO song_preferences(song_id,policy) SELECT id,'blocked' FROM songs;
COMMIT;`, preferences); err != nil {
			t.Fatal(err)
		}
	}
	var local LocalCatalog
	local.Files.References = make(map[string]string, resources)
	local.Files.Present = make(map[string]bool, resources)
	for i := range resources {
		key := fmt.Sprintf("%032x", i+1)
		local.Files.References[strconv.Itoa(9000000+i)] = key
		local.Files.Present[key] = true
	}
	return &Server{Store: store, cfg: Config{CacheUnlimited: true}}, local
}

func TestDownloadFillPolicyAllocationsIndependentOfUnrelatedPreferences(t *testing.T) {
	measure := func(preferences int) float64 {
		s, local := fillPolicyFixture(t, 8, preferences)
		return testing.AllocsPerRun(3, func() {
			result, err := s.NewDownloadFill(t.Context(), false, false).Run(local, nil, nil)
			if err != nil || result.Hits != 8 || result.Checked != 8 || result.Skipped != 0 || result.Failed != 0 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
	empty, populated := measure(0), measure(1000)
	// Allow fixed SQLite/runtime overhead; unrelated preference rows must not
	// be materialized again for every resource, even when it is a local hit.
	if populated > empty+1000 {
		t.Fatalf("fill allocations grow with unrelated preferences: empty=%.0f populated=%.0f", empty, populated)
	}
}

func TestDownloadFillReadsCurrentPoliciesBeforeLocalHits(t *testing.T) {
	for _, policy := range []SongPolicy{SongBlocked, SongNormal} {
		t.Run(string(policy), func(t *testing.T) {
			s, local := fillPolicyFixture(t, 2, 0)
			if policy == SongNormal {
				if err := s.Store.SetSongPolicy(t.Context(), 9000000, SongBlocked); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.NewDownloadFill(t.Context(), false, false).Run(local, nil, func(progress DownloadFillResult) {
				// Both songs have the same score; the newer ID is processed first.
				if progress.Checked == 1 {
					if err := s.Store.SetSongPolicy(t.Context(), 9000000, policy); err != nil {
						t.Error(err)
					}
				}
			})
			wantHits, wantSkipped := 2, 0
			if policy == SongBlocked {
				wantHits, wantSkipped = 1, 1
			}
			if err != nil || result.Checked != 2 || result.Hits != wantHits || result.Skipped != wantSkipped || result.Failed != 0 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestDownloadFillRechecksPolicyAtAdmission(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("blocked song reached upstream")
		http.Error(w, "unexpected download", http.StatusInternalServerError)
	}, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			t.Error("blocked song resolved an address")
			return "", fmt.Errorf("unexpected address lookup")
		}
	})
	local := fillTestCatalog(t, s, []CatalogSong{{ID: 9000000, MD5: videoKey(payload)}})
	attempted := false
	result, err := s.NewDownloadFill(t.Context(), false, false).Run(local, func(ctx context.Context, id int64) (string, error) {
		attempted = true
		// The per-resource snapshot has already been read; admission must
		// observe this newer preference before resolving or creating demand.
		if err := s.Store.SetSongPolicy(ctx, id, SongBlocked); err != nil {
			return "", err
		}
		return s.PrefetchDownload(ctx, id)
	}, nil)
	if err != nil || !attempted || result.Checked != 1 || result.Skipped != 1 || result.Hits != 0 || result.Downloaded != 0 || result.Failed != 0 {
		t.Fatalf("result=%+v attempted=%v error=%v", result, attempted, err)
	}
	var demands int
	if err := s.Store.db.QueryRow("SELECT COALESCE(SUM(demand_count),0) FROM song_usage").Scan(&demands); err != nil || demands != 0 {
		t.Fatalf("blocked song demands=%d error=%v", demands, err)
	}
}

func BenchmarkDownloadFillPolicies(b *testing.B) {
	for _, preferences := range []int{0, 1000} {
		b.Run(fmt.Sprintf("preferences=%d", preferences), func(b *testing.B) {
			s, local := fillPolicyFixture(b, 5000, preferences)
			b.ReportAllocs()
			for b.Loop() {
				result, err := s.NewDownloadFill(b.Context(), false, false).Run(local, nil, nil)
				if err != nil || result.Hits != 5000 || result.Checked != 5000 || result.Skipped != 0 || result.Failed != 0 {
					b.Fatalf("result=%+v error=%v", result, err)
				}
			}
		})
	}
}
