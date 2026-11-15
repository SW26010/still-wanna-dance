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

func TestDownloadRegistrationConflictRecovers(t *testing.T) {
	for _, conflict := range []string{"busy", "constraint", "mapping_constraint"} {
		t.Run(conflict, func(t *testing.T) {
			other := strings.Repeat("x", len(payload))
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body := payload
				if r.URL.Query().Get("e") == videoKey(other) {
					body = other
				}
				serveFixtureContent(w, r, body)
			})
			s.Store.usage.flush()
			wantCode := sqlite3.SQLITE_CONSTRAINT
			var release func()
			if conflict == "busy" {
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
				release = func() {
					if _, err := locker.Exec("ROLLBACK"); err != nil {
						t.Fatal(err)
					}
				}
				wantCode = sqlite3.SQLITE_BUSY
			} else {
				table := "media"
				if conflict == "mapping_constraint" {
					table = "song_media"
				}
				if _, err := s.Store.db.Exec("CREATE TRIGGER fail_media BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'index conflict'); END"); err != nil {
					t.Fatal(err)
				}
				release = func() {
					if _, err := s.Store.db.Exec("DROP TRIGGER fail_media"); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, writeErr := s.PrefetchSong(t.Context(), "41", videoURL(payload))
			var databaseErr *sqlite.Error
			if !errors.As(writeErr, &databaseErr) || databaseErr.Code()&0xff != wantCode {
				t.Fatalf("registration error=%v, want SQLite code %d", writeErr, wantCode)
			}
			if body, err := os.ReadFile(s.cfg.videoFile(videoKey(payload))); err != nil || string(body) != payload {
				t.Fatalf("download was not published before registration failed: %v", err)
			}
			release()
			if err := s.Store.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
				t.Fatal("SQL did not recover after conflict ended", err)
			}
			before := calls.Load()
			if source, err := s.PrefetchSong(t.Context(), "43", videoURL(other)); err != nil || source != "MISS" {
				t.Fatalf("cold download rejected after SQL recovered: source=%s error=%v upstream calls=%d", source, err, calls.Load()-before)
			}
			if calls.Load() <= before {
				t.Fatal("new resource did not reach upstream")
			}
			var size int64
			if err := s.Store.db.QueryRow("SELECT byte_size FROM media JOIN song_media USING(md5) WHERE md5=? AND song_id=43", videoKey(other)).Scan(&size); err != nil || size != int64(len(other)) {
				t.Fatalf("new resource registration: size=%d error=%v", size, err)
			}
			if source, err := s.Prefetch(t.Context(), videoURL(payload)); source != "HIT" || err != nil {
				t.Fatal("published cache unavailable", source, err)
			}
			if errors.Is(writeErr, ErrLocalStorage) || s.StorageError() != nil {
				t.Fatalf("conflict retained as terminal: write=%v retained=%v", writeErr, s.StorageError())
			}
			for _, failed := range []<-chan struct{}{s.Store.FailureDone(), s.LocalFailureDone()} {
				select {
				case <-failed:
					t.Fatal("conflict signaled terminal failure")
				default:
				}
			}
		})
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

func TestDownloadDatabaseReadConflictRecovers(t *testing.T) {
	for _, operation := range []string{"download", "queue", "publication", "fill"} {
		t.Run(operation, func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
			s.Store.usage.flush()
			if _, err := s.Store.db.Exec("PRAGMA busy_timeout=0"); err != nil {
				t.Fatal(err)
			}
			locker, err := openFixtureDatabase(s.Store.path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Close()
			// Unlike the registration test's writer lock, this also blocks reads.
			if _, err := locker.Exec("BEGIN EXCLUSIVE"); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch operation {
			case "download":
				_, err = s.Prefetch(ctx, videoURL(payload))
			case "queue":
				_, err = s.queueTimings(ctx, []int64{42})
			case "publication":
				_, err = s.reservePublication(ctx, video{key: videoKey(payload), size: int64(len(payload))})
			case "fill":
				_, err = s.NewDownloadFill(ctx, false, true).fillResources(ctx, map[string]string{"42": videoKey(payload)})
			}
			var databaseErr *sqlite.Error
			if !errors.As(err, &databaseErr) || databaseErr.Code()&0xff != sqlite3.SQLITE_BUSY || errors.Is(err, ErrLocalStorage) {
				t.Fatalf("read conflict=%v, want nonterminal SQLITE_BUSY", err)
			}
			if _, err := locker.Exec("ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if source, err := s.Prefetch(ctx, videoURL(payload)); err != nil || source != "MISS" {
				t.Fatalf("cold download after read conflict: source=%s error=%v", source, err)
			}
			if s.StorageError() != nil {
				t.Fatal("read conflict retained", s.StorageError())
			}
		})
	}
}

func TestDownloadRegistrationStorageFailureIsRetained(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		serveFixtureContent(w, r, payload)
	})
	s.Store.usage.flush()
	if _, err := s.Store.db.Exec("PRAGMA query_only=1"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Prefetch(t.Context(), videoURL(payload))
	var databaseErr *sqlite.Error
	if !errors.As(err, &databaseErr) || databaseErr.Code()&0xff != sqlite3.SQLITE_READONLY || !errors.Is(err, ErrLocalStorage) {
		t.Fatalf("registration error=%v, want terminal SQLITE_READONLY", err)
	}
	if !errors.Is(s.Store.Error(), ErrLocalStorage) {
		t.Fatal("Store did not retain database failure", s.Store.Error())
	}
	select {
	case <-s.Store.FailureDone():
	default:
		t.Fatal("Store did not notify database failure")
	}
	if _, err := s.Store.db.Exec("PRAGMA query_only=0"); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := s.Prefetch(t.Context(), videoURL("new resource")); !errors.Is(err, ErrLocalStorage) || calls.Load() != before {
		t.Fatalf("terminal failure allowed new download: error=%v calls=%d", err, calls.Load()-before)
	}
}
