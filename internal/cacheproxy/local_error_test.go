package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"still-wanna-dance/internal/blocksched"
)

func TestMetadataBusyDoesNotDisableColdDownloads(t *testing.T) {
	for _, operation := range []string{"playback_address", "catalog"} {
		t.Run(operation, func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				serveFixtureContent(w, r, payload)
			})
			s.Store.usage.flush()
			write := func() error {
				if operation == "playback_address" {
					return s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 42, Node: "cf", URL: videoURL(payload)})
				}
				return s.Store.SyncCatalog(t.Context(), testCandidate("20261112000000", CatalogSong{ID: 42, MD5: videoKey(payload)}))
			}
			if _, err := s.Store.db.Exec("PRAGMA busy_timeout=0"); err != nil {
				t.Fatal(err)
			}
			locker, err := openFixtureDatabase(s.Store.path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Close()
			if _, err := locker.Exec("BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			writeErr := write()
			var databaseErr *sqlite.Error
			if !errors.As(writeErr, &databaseErr) || databaseErr.Code()&0xff != sqlite3.SQLITE_BUSY {
				t.Fatalf("metadata write error=%v, want SQLITE_BUSY", writeErr)
			}
			if _, err := locker.Exec("ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if err := write(); err != nil {
				t.Fatal("SQL did not recover after lock released", err)
			}
			if source, err := s.Prefetch(t.Context(), videoURL(payload)); err != nil || source != "MISS" {
				t.Fatalf("cold download rejected after SQL recovered: source=%s error=%v", source, err)
			}
			if errors.Is(writeErr, ErrLocalStorage) || s.StorageError() != nil {
				t.Fatalf("temporary conflict marked as terminal: write=%v retained=%v", writeErr, s.StorageError())
			}
			for _, failed := range []<-chan struct{}{s.Store.FailureDone(), s.LocalFailureDone()} {
				select {
				case <-failed:
					t.Fatal("temporary conflict signaled terminal failure")
				default:
				}
			}
		})
	}
}

func TestFixedResourceFillReportsAddressStorageFailure(t *testing.T) {
	var calls atomic.Int32
	var s *Server
	s, _ = setup(t, nil, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			calls.Add(1)
			url := videoURL(payload)
			return url, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: url})
		}
	})
	target := seedPreview(t, s, payload)
	if _, err := s.Store.db.Exec("INSERT INTO songs(id) VALUES(2); INSERT INTO song_media(song_id,md5) VALUES(2,?)", target.Resource); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.db.Exec("PRAGMA query_only=1"); err != nil {
		t.Fatal(err)
	}
	_, err := s.PrefetchDownload(withExpectedMD5(t.Context(), target.Resource), 1)
	if !errors.Is(err, ErrLocalStorage) || errors.Is(err, errPlaybackUpstream) || !errors.Is(s.StorageError(), ErrLocalStorage) || calls.Load() != 1 {
		t.Fatalf("error=%v retained=%v API calls=%d", err, s.StorageError(), calls.Load())
	}
}

func TestPlaybackCheckKeepsCompletedStorageFailureOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	check := &playbackCheck{done: make(chan struct{}), err: fmt.Errorf("address write: %w", ErrLocalStorage)}
	close(check.done)
	s := &Server{}
	for range 100 {
		if _, err := s.waitSongQuery(ctx, check); !errors.Is(err, ErrLocalStorage) {
			t.Fatalf("completed storage failure was replaced: %v", err)
		}
	}
}

func TestStorageFailureStopsDownloadsButKeepsHits(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); serveFixtureContent(w, r, payload) }, func(cfg *Config) { fixtureFaultableBlocks(t, cfg) })
	ctx := context.Background()
	if _, err := s.Prefetch(ctx, videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.cfg.tempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfg.tempDir(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	other := videoURL(strings.Repeat("x", len(payload)))
	if _, err := s.Prefetch(ctx, other); !errors.Is(err, ErrLocalStorage) {
		t.Fatal(err)
	}
	before := calls.Load()
	for range 3 {
		if _, err := s.Prefetch(ctx, other); !errors.Is(err, ErrLocalStorage) {
			t.Fatal(err)
		}
	}
	if calls.Load() != before {
		t.Fatal("restarted failed download")
	}
	if source, err := s.Prefetch(ctx, videoURL(payload)); err != nil || source != "HIT" {
		t.Fatal(source, err)
	}
	s.mu.Lock()
	active := len(s.flights)
	s.mu.Unlock()
	if active != 0 {
		t.Fatal("failed flight retained capacity")
	}
}

func TestMetadataStorageFailureIsRetained(t *testing.T) {
	for _, operation := range []string{"playback_address", "catalog"} {
		for _, fault := range []struct {
			name, query string
			code        int
		}{
			{"readonly", "PRAGMA query_only=1", sqlite3.SQLITE_READONLY},
			{"full", `CREATE TABLE fill(data BLOB);
CREATE TRIGGER fill_disk BEFORE INSERT ON media BEGIN INSERT INTO fill VALUES(zeroblob(1048576)); END;
VACUUM;
PRAGMA max_page_count=1`, sqlite3.SQLITE_FULL},
		} {
			t.Run(operation+"/"+fault.name, func(t *testing.T) {
				s, _ := setup(t, func(http.ResponseWriter, *http.Request) {})
				s.Store.usage.flush()
				if _, err := s.Store.db.Exec(fault.query); err != nil {
					t.Fatal(err)
				}
				var err error
				if operation == "playback_address" {
					err = s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 42, URL: videoURL(payload), Node: "cf"})
				} else {
					err = s.Store.SyncCatalog(t.Context(), testCandidate("20261112000000", CatalogSong{ID: 42, MD5: videoKey(payload)}))
				}
				var databaseErr *sqlite.Error
				if !errors.As(err, &databaseErr) || databaseErr.Code()&0xff != fault.code || !errors.Is(err, ErrLocalStorage) || !errors.Is(s.StorageError(), ErrLocalStorage) {
					t.Fatalf("storage failure not retained: error=%v retained=%v, want SQLite code %d", err, s.StorageError(), fault.code)
				}
				select {
				case <-s.Store.FailureDone():
				default:
					t.Fatal("storage failure did not notify waiters")
				}
				if _, err := s.Prefetch(t.Context(), videoURL(payload)); !errors.Is(err, ErrLocalStorage) {
					t.Fatal("storage failure did not stop cold download", err)
				}
			})
		}
	}
}

func TestIndexFailureStopsNewDownloads(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); serveFixtureContent(w, r, payload) })
	if _, err := s.Store.usage.db.Exec(`CREATE TRIGGER fail_media BEFORE INSERT ON media BEGIN SELECT RAISE(FAIL, 'index failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); !errors.Is(err, ErrLocalStorage) {
		t.Fatal(err)
	}
	if _, err := s.Prefetch(context.Background(), videoURL(strings.Repeat("x", len(payload)))); !errors.Is(err, ErrLocalStorage) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("download repeated after index failure", calls.Load())
	}
	if source, err := s.Prefetch(context.Background(), videoURL(payload)); source != "HIT" || err != nil {
		t.Fatal("published cache unavailable", source, err)
	}
}

func TestDownloadCleanupErrorPriority(t *testing.T) {
	local := &os.PathError{Op: "write", Path: "block", Err: os.ErrClosed}
	upstream := fmt.Errorf("%w: failed block", errUpstreamDownload)
	for _, first := range []error{nil, upstream, context.Canceled, context.DeadlineExceeded, blocksched.ErrClosed, errBusy} {
		for _, localFirst := range []bool{false, true} {
			errorsInOrder := []error{first, local}
			if localFirst {
				errorsInOrder[0], errorsInOrder[1] = errorsInOrder[1], errorsInOrder[0]
			}
			var result error
			for _, err := range errorsInOrder {
				result = preferDownloadError(result, err)
			}
			s := &Server{Store: &Store{}}
			result = s.storageError(result)
			if !errors.Is(result, os.ErrClosed) || !errors.Is(result, ErrLocalStorage) || s.StorageError() == nil || errors.Is(result, errUpstreamDownload) {
				t.Fatalf("order=%v localFirst=%v lost local failure: %v", first, localFirst, result)
			}
		}
	}
	for _, next := range []error{nil, context.Canceled, context.DeadlineExceeded, blocksched.ErrClosed} {
		if err := preferDownloadError(upstream, next); err != upstream {
			t.Fatal("routine cleanup replaced the upstream failure", err)
		}
	}
}
