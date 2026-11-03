package cacheproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Attach an engine without its background loop so wakeups can be observed
// without competing with a retention pass for the same coalescing channel.
func retentionChangeEngine(t *testing.T) *Server {
	t.Helper()
	store, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "business.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, cfg: fixtureConfig(), retentionWake: make(chan struct{}, 1), versionPins: make(map[string]int)}
	if err := store.attach(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.detach(s)
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func requireRetentionWake(t *testing.T, s *Server, want bool) {
	t.Helper()
	select {
	case <-s.retentionWake:
		if !want {
			t.Fatal("failed write woke retention")
		}
	default:
		if want {
			t.Fatal("completed write did not wake retention")
		}
	}
}

func requireQueueWake(t *testing.T, changed <-chan struct{}, want bool) {
	t.Helper()
	select {
	case <-changed:
		if !want {
			t.Fatal("write unexpectedly woke queue preparation")
		}
	default:
		if want {
			t.Fatal("preference write did not wake queue preparation")
		}
	}
}

func TestBusinessWritesNotifyRetentionAndQueue(t *testing.T) {
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name      string
		prepare   func(*testing.T, *Server)
		write     func(*Server) error
		wantErr   error
		wantQueue bool
		verify    func(*testing.T, *Server)
	}{
		{
			name: "catalog",
			write: func(s *Server) error {
				return s.SyncCatalog(context.Background(), testCandidate("20261002000000", CatalogSong{ID: 1, MD5: key}))
			},
		},
		{
			name: "older catalog still commits a mapping",
			prepare: func(t *testing.T, s *Server) {
				if err := s.SyncCatalog(t.Context(), testCandidate("20261002000000", CatalogSong{ID: 1})); err != nil {
					t.Fatal(err)
				}
			},
			write: func(s *Server) error {
				return s.SyncCatalog(context.Background(), testCandidate("20261001000000", CatalogSong{ID: 2, MD5: key}))
			},
			wantErr: ErrCatalogOlder,
			verify:  func(t *testing.T, s *Server) { assertSongResource(t, s, "2", key) },
		},
		{
			name: "observed URL",
			write: func(s *Server) error {
				return s.Store.ObserveSongURL(context.Background(), PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"})
			},
		},
		{
			name:      "song preference",
			write:     func(s *Server) error { return s.SetSongPolicy(context.Background(), 1, SongFavorite) },
			wantQueue: true,
		},
		{
			name: "recorded existing mapping",
			prepare: func(t *testing.T, s *Server) {
				if _, err := s.db.Exec("INSERT INTO media(md5) VALUES (?)", key); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("INSERT INTO songs(id) VALUES (1)"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (1,?)", key); err != nil {
					t.Fatal(err)
				}
				// Keep the worker's release from providing an unrelated last-pin wake.
				v := video{key: key}
				s.pinVideo(v)
				t.Cleanup(func() { s.releaseVideo(v) })
			},
			write: func(s *Server) error { return s.recordSongVideo(context.Background(), "1", video{key: key}) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := retentionChangeEngine(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			select {
			case <-s.retentionWake:
			default:
			}
			changed := s.QueuePreparationChanged()
			err := tc.write(s)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("write returned %v, want %v", err, tc.wantErr)
			}
			requireRetentionWake(t, s, true)
			requireQueueWake(t, changed, tc.wantQueue)
			if tc.verify != nil {
				tc.verify(t, s)
			}
		})
	}
}

func TestFailedBusinessWritesDoNotNotify(t *testing.T) {
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, *Server)
		table   string
		write   func(*Server) error
	}{
		{
			name: "catalog",
			write: func(s *Server) error {
				return s.SyncCatalog(context.Background(), testCandidate("20261002000000", CatalogSong{ID: 1, MD5: key}))
			},
		},
		{
			name: "observed URL",
			write: func(s *Server) error {
				return s.Store.ObserveSongURL(context.Background(), PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"})
			},
		},
		{
			name:  "song preference",
			table: "song_preferences",
			write: func(s *Server) error { return s.SetSongPolicy(context.Background(), 1, SongFavorite) },
		},
		{
			name: "recorded mapping",
			prepare: func(t *testing.T, s *Server) {
				if _, err := s.db.Exec("INSERT INTO media(md5) VALUES (?)", key); err != nil {
					t.Fatal(err)
				}
				v := video{key: key}
				s.pinVideo(v)
				t.Cleanup(func() { s.releaseVideo(v) })
			},
			write: func(s *Server) error { return s.recordSongVideo(context.Background(), "1", video{key: key}) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := retentionChangeEngine(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			table := tc.table
			if table == "" {
				table = "song_media"
			}
			if _, err := s.db.Exec("CREATE TRIGGER reject_write BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'injected failure'); END"); err != nil {
				t.Fatal(err)
			}
			changed := s.QueuePreparationChanged()
			if err := tc.write(s); err == nil {
				t.Fatal("injected write failure was ignored")
			}
			requireRetentionWake(t, s, false)
			requireQueueWake(t, changed, false)
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM songs WHERE id=1").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed transaction left a placeholder", count, err)
			}
		})
	}
}

func TestPreferenceCommitHoldsRetentionExclusionThroughNotification(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	changed := s.QueuePreparationChanged()
	s.retentionMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.retentionMu.Unlock()
		}
	}()
	before := s.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- s.SetSongPolicy(ctx, 1, SongBlocked) }()
	waitForDatabaseWait(t, s.db, before)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// The queued writer owns the only connection and commits before this read.
	var policy SongPolicy
	if err := s.db.QueryRowContext(ctx, "SELECT policy FROM song_preferences WHERE song_id=1").Scan(&policy); err != nil || policy != SongBlocked {
		t.Fatal("preference did not commit", policy, err)
	}
	if s.retentionRunMu.TryLock() {
		s.retentionRunMu.Unlock()
		t.Fatal("retention could run between commit and notification")
	}
	requireQueueWake(t, changed, false)
	s.retentionMu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("preference write did not finish", ctx.Err())
	}
	requireRetentionWake(t, s, true)
	requireQueueWake(t, changed, true)
}

func TestPreferenceFailureIsRecordedBeforeRetentionResumes(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.db.Exec("PRAGMA query_only=1"); err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := s.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- s.SetSongPolicy(ctx, 1, SongBlocked) }()
	waitForDatabaseWait(t, s.db, before)
	s.Store.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.Store.mu.Unlock()
		}
	}()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// Reacquiring the only connection waits for the failed transaction to end.
	returned, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := returned.Close(); err != nil {
		t.Fatal(err)
	}
	if s.retentionRunMu.TryLock() {
		s.retentionRunMu.Unlock()
		t.Fatal("retention could resume before the storage failure was recorded")
	}
	s.Store.mu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err == nil || !errors.Is(s.Store.Error(), ErrLocalStorage) {
			t.Fatal("write failure was not retained", err, s.Store.Error())
		}
	case <-ctx.Done():
		t.Fatal("failed preference write did not finish", ctx.Err())
	}
	requireRetentionWake(t, s, false)
}

func TestMappingWriteSerializesWithRetention(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	requireRetentionWake(t, s, true)
	path := filepath.Join(t.TempDir(), key+".mp4")
	putRetained(t, path, 12)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.CacheUnlimited, s.cfg.VideoBudgetBytes = false, 0
	s.retained = map[string]retainedVideo{key: {path: path, key: key, size: info.Size(), modified: info.ModTime().UnixNano()}}
	s.retainedBytes = info.Size()

	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := s.db.Stats().WaitCount
	written := make(chan error, 1)
	go func() {
		written <- s.Store.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"})
	}()
	waitForDatabaseWait(t, s.db, before)
	if s.retentionRunMu.TryLock() {
		s.retentionRunMu.Unlock()
		t.Fatal("mapping write did not exclude retention")
	}
	started, retained := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		s.runRetention(false)
		close(retained)
	}()
	<-started
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("mapping write did not finish", ctx.Err())
	}
	select {
	case <-retained:
	case <-ctx.Done():
		t.Fatal("retention did not finish", ctx.Err())
	}
	expectRetained(t, path, true)
}

func TestPreferenceWriteSerializesWithPublication(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.Store.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"}); err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := s.db.Stats().WaitCount
	written := make(chan error, 1)
	go func() { written <- s.SetSongPolicy(ctx, 1, SongBlocked) }()
	waitForDatabaseWait(t, s.db, before)
	if s.retentionRunMu.TryLock() {
		s.retentionRunMu.Unlock()
		t.Fatal("preference write did not exclude publication")
	}
	started, admitted := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		r, err := s.reservePublication(ctx, video{key: key, size: 12})
		if r != nil {
			s.releasePublication(r)
		}
		admitted <- err
	}()
	<-started
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("preference write did not finish", ctx.Err())
	}
	select {
	case err := <-admitted:
		if !errors.Is(err, ErrNotRetained) {
			t.Fatal("publication ignored the committed block", err)
		}
	case <-ctx.Done():
		t.Fatal("publication did not finish", ctx.Err())
	}
}
