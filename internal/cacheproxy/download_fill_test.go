package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fillTestCatalog(t *testing.T, s *Server, songs []CatalogSong) LocalCatalog {
	t.Helper()
	if err := s.Store.SyncCatalog(t.Context(), Catalog{Revision: "20261028000000", Source: "test", Songs: songs}); err != nil {
		t.Fatal(err)
	}
	local, release, err := s.ReadLocalCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return local
}

func TestDownloadFillTriesAllSharedSongCandidates(t *testing.T) {
	var mu sync.Mutex
	var attempts []string
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, payload)
	}, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(_ context.Context, id string) (string, error) {
			mu.Lock()
			attempts = append(attempts, id)
			mu.Unlock()
			if id != "1" {
				return "", errors.New("song address unavailable")
			}
			return videoURL(payload), nil
		}
	})
	songs := make([]CatalogSong, 9)
	for i := range songs {
		songs[i] = CatalogSong{ID: int64(i + 1), MD5: videoKey(payload)}
	}
	local := fillTestCatalog(t, s, songs)
	result, err := s.NewDownloadFill(t.Context(), false, false).Run(local, s.PrefetchDownload, nil)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || result.Total != 9 || result.Downloaded != 9 || result.Failed != 0 || len(attempts) != 9 || attempts[8] != "1" {
		t.Fatalf("result=%+v attempts=%v error=%v", result, attempts, err)
	}
}

func TestDownloadFillUsesBlockedSelectedSongAsAddressHint(t *testing.T) {
	var mu sync.Mutex
	var attempts []string
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, payload)
	}, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(_ context.Context, id string) (string, error) {
			mu.Lock()
			attempts = append(attempts, id)
			mu.Unlock()
			if id != "1" {
				return "", errors.New("song address unavailable")
			}
			return videoURL(payload), nil
		}
	})
	local := fillTestCatalog(t, s, []CatalogSong{
		{ID: 1, MD5: videoKey(payload)},
		{ID: 2, MD5: videoKey(payload)},
	})
	if err := s.Store.SetSongPolicy(t.Context(), 1, SongBlocked); err != nil {
		t.Fatal(err)
	}
	result, err := s.NewDownloadFill(t.Context(), false, false).Run(local, s.PrefetchDownload, nil)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || result.Total != 2 || result.Checked != 2 || result.Skipped != 1 || result.Downloaded != 1 || result.Failed != 0 || !reflect.DeepEqual(attempts, []string{"2", "1"}) {
		t.Fatalf("result=%+v attempts=%v error=%v", result, attempts, err)
	}
	var blockedDemands int
	if err := s.Store.db.QueryRow("SELECT COALESCE(SUM(demand_count), 0) FROM song_usage WHERE song_id=1").Scan(&blockedDemands); err != nil || blockedDemands != 0 {
		t.Fatalf("blocked song demands=%d error=%v", blockedDemands, err)
	}
}

func TestDownloadFillFavoriteOrderIncludesUnselectedAliasesAndUnresolvedSongs(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(fmt.Sprint(retain), func(t *testing.T) {
			s, _ := setup(t, nil, func(c *Config) { c.RetainFavorites = retain })
			shared, unresolved, ordinary := videoKey("shared"), videoKey("unresolved"), videoKey("ordinary")
			local := fillTestCatalog(t, s, []CatalogSong{
				{ID: 9000001, MD5: shared}, {ID: 9000002},
				{ID: 9000003, MD5: ordinary}, {ID: 9000004, MD5: shared},
			})
			// The selected ordinary alias inherits its resource's favorite priority.
			delete(local.Files.References, "9000004")
			for _, id := range []int64{9000002, 9000004} {
				if err := s.Store.SetSongPolicy(t.Context(), id, SongFavorite); err != nil {
					t.Fatal(err)
				}
			}
			setRetentionLimit(t, s, 10)
			keys := map[int64]string{9000001: shared, 9000002: unresolved, 9000003: ordinary}
			var attempts []int64
			result, err := s.NewDownloadFill(t.Context(), false, retain).Run(local, func(ctx context.Context, id int64) (string, error) {
				attempts = append(attempts, id)
				finish, err := s.reserveBatchVideo(ctx, video{key: keys[id], size: 5})
				if err != nil {
					return "", err
				}
				finish(true)
				return "MISS", nil
			}, nil)
			want := []int64{9000003, 9000002, 9000001}
			if retain {
				want = []int64{9000002, 9000001, 9000003}
			}
			if err != nil || !reflect.DeepEqual(attempts, want) || !result.BudgetReached || result.Downloaded != 2 || result.Failed != 0 {
				t.Fatalf("attempts=%v result=%+v error=%v", attempts, result, err)
			}
		})
	}
}

func TestDownloadFillDoesNotResolveUnselectedAliases(t *testing.T) {
	const firstID, nextID = int64(9000100), int64(9000001)
	staleLookup, nextLookup := make(chan string, 1), make(chan struct{}, 1)
	s, _ := setup(t, nil, func(cfg *Config) {
		cfg.CacheUnlimited = true
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			switch id {
			case strconv.FormatInt(firstID, 10):
				return "", errors.New("first song unavailable")
			case strconv.FormatInt(nextID, 10):
				nextLookup <- struct{}{}
				return "", errors.New("next song unavailable")
			default:
				staleLookup <- id
				<-ctx.Done()
				return "", ctx.Err()
			}
		}
	})
	local := fillTestCatalog(t, s, []CatalogSong{
		{ID: firstID, MD5: videoKey(payload)},
		{ID: nextID, MD5: videoKey("next resource")},
	})
	for id := int64(9000200); id < 9000220; id++ {
		if _, err := s.Store.db.Exec("INSERT INTO songs(id) VALUES (?)", id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Store.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (?,?)", id, videoKey(payload)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan DownloadFillResult, 1)
	go func() {
		result, _ := s.NewDownloadFill(ctx, false, false).Run(local, s.PrefetchDownload, nil)
		done <- result
	}()
	select {
	case id := <-staleLookup:
		cancel()
		<-done
		t.Fatalf("unselected song %s blocked the next resource", id)
	case <-nextLookup:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("next resource was not processed")
	}
	select {
	case result := <-done:
		if result.Checked != 2 || result.Failed != 2 {
			t.Fatalf("unexpected batch result: %+v", result)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("batch did not finish")
	}
}

func TestDownloadFillCanResolveSelectedHistoricalSongs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target DownloadTarget
		want   []string
	}{
		{"song", DownloadTarget{SongID: "9000200"}, []string{"9000200"}},
		{"resource", DownloadTarget{Resource: videoKey(payload)}, []string{"9000201", "9000200"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }, func(cfg *Config) {
				cfg.CacheUnlimited = true
				cfg.ResolvePlayback = func(_ context.Context, id string) (string, error) {
					mu.Lock()
					attempts = append(attempts, id)
					mu.Unlock()
					if id != "9000200" {
						return "", errors.New("song address unavailable")
					}
					return videoURL(payload), nil
				}
			})
			fillTestCatalog(t, s, []CatalogSong{{ID: 9000100, MD5: videoKey("current resource")}})
			if _, err := s.Store.db.Exec("INSERT INTO media(md5,byte_size) VALUES (?,?)", videoKey(payload), len(payload)); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{9000200, 9000201} {
				if _, err := s.Store.db.Exec("INSERT INTO songs(id) VALUES (?)", id); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Store.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (?,?)", id, videoKey(payload)); err != nil {
					t.Fatal(err)
				}
			}
			local, release, err := s.ReadDownloadTarget(t.Context(), tc.target)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			result, err := s.NewDownloadFill(t.Context(), false, false).Run(local, s.PrefetchDownload, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || result.Downloaded != len(tc.want) || result.Failed != 0 || !reflect.DeepEqual(attempts, tc.want) {
				t.Fatalf("result=%+v attempts=%v want=%v error=%v", result, attempts, tc.want, err)
			}
		})
	}
}

func TestDownloadFillRetriesAdmittedResourceAfterBudgetStop(t *testing.T) {
	const firstID, otherID, retainedID = 9000004, 9000003, 9000001
	budgetStop := make(chan struct{})
	var retry atomic.Bool
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("e") != videoKey(payload) {
			t.Error("over-budget resource reached upstream")
		}
		if !retry.Load() {
			select {
			case <-budgetStop:
			case <-r.Context().Done():
				return
			}
			http.Error(w, "first candidate failed after another resource exhausted budget", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, payload)
	}, func(cfg *Config) {
		cfg.CacheUnlimited, cfg.VideoBudgetBytes = false, int64(len(payload))
	})
	// The current aliases rank this resource first. Retry must preserve this
	// resource's budget after the first candidate was admitted and then failed.
	if err := s.Store.SyncCatalog(t.Context(), Catalog{Revision: "20261027000000", Source: "test", Songs: []CatalogSong{{ID: retainedID, MD5: videoKey(payload)}}}); err != nil {
		t.Fatal(err)
	}
	local := fillTestCatalog(t, s, []CatalogSong{
		{ID: firstID, MD5: videoKey(payload)},
		{ID: otherID, MD5: videoKey("another resource")},
		{ID: retainedID, MD5: videoKey(payload)},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var calls []int64
	var latest DownloadFillResult
	result, err := s.NewDownloadFill(ctx, false, false).Run(local, func(ctx context.Context, id int64) (string, error) {
		mu.Lock()
		calls = append(calls, id)
		mu.Unlock()
		switch id {
		case firstID:
			if _, err := s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL(payload)); !errors.Is(err, errUpstreamDownload) {
				return "", err
			}
			retry.Store(true)
			return s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL(payload))
		case otherID:
			mu.Lock()
			if latest.Checked != 0 || latest.Downloaded != 0 {
				t.Errorf("admission was counted as completion: %+v", latest)
			}
			mu.Unlock()
			source, err := s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL("another resource"))
			if !errors.Is(err, ErrBatchBudget) {
				t.Errorf("expected budget refusal while first resource is unfinished, got %v", err)
			}
			close(budgetStop)
			return source, err
		default:
			return "", fmt.Errorf("unexpected candidate %d", id)
		}
	}, func(progress DownloadFillResult) {
		mu.Lock()
		defer mu.Unlock()
		latest = progress
	})
	if err != nil {
		t.Fatal(err)
	}
	want := DownloadFillResult{Total: 3, Checked: 2, Downloaded: 2, BudgetReached: true}
	if !reflect.DeepEqual(result, want) || !reflect.DeepEqual(latest, want) {
		t.Fatalf("result=%+v progress=%+v, want %+v", result, latest, want)
	}
	if !reflect.DeepEqual(calls, []int64{firstID, otherID}) {
		t.Fatal("lost resource or candidate order", calls)
	}
	data, err := os.ReadFile(cfg.videoFile(videoKey(payload)))
	if err != nil || string(data) != payload {
		t.Fatal("retry did not publish the shared resource", string(data), err)
	}
}

func TestDownloadFillCancellationWaitsForRetrySettlement(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected download") })
	local := fillTestCatalog(t, s, []CatalogSong{
		{ID: 9000003, MD5: videoKey(payload)}, {ID: 9000002, MD5: videoKey(payload)},
		{ID: 9000001, MD5: videoKey("next resource")},
	})
	ctx, cancel := context.WithCancel(t.Context())
	retrying, settling, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer cancel()
	defer close(release)
	done := make(chan struct{})
	var result DownloadFillResult
	var resultErr error
	go func() {
		defer close(done)
		result, resultErr = s.NewDownloadFill(ctx, false, false).Run(local, func(ctx context.Context, id int64) (string, error) {
			switch id {
			case 9000003:
				// Fixed-resource preparation owns its retries until settlement.
				close(retrying)
				<-ctx.Done()
				close(settling)
				<-release
				return "", ctx.Err()
			default:
				t.Error("resolved next resource before the current one admitted or finished")
				return "", errors.New("unexpected candidate")
			}
		}, nil)
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("fill stalled")
		}
	}
	wait(retrying)
	cancel()
	wait(settling)
	select {
	case <-done:
		t.Fatal("fill returned before the canceled retry settled")
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	wait(done)
	if !errors.Is(resultErr, context.Canceled) || result.Total != 3 || result.Checked != 0 || result.Failed != 0 || result.CurrentSongID != "9000003" {
		t.Fatalf("cancellation changed partial results: %+v err=%v", result, resultErr)
	}
}
