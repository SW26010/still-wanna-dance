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
	s.binding = &retentionBinding{changed: s.databaseChanged}
	if err := store.attach(s.binding); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.detach(s.binding)
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

// Hold the existing usage flush, reached after policy lookup and before the
// retention decision is applied. This fixture has no real usage worker.
func requireDecisionBeforeWrite(t *testing.T, s *Server, decision, write func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	u := s.Store.usage
	u.mu.Lock()
	if u.started {
		u.mu.Unlock()
		t.Fatal("usage worker is already running")
	}
	u.started = true
	u.mu.Unlock()

	decisionDone := make(chan struct{})
	var decisionErr error
	go func() { decisionErr = decision(ctx); close(decisionDone) }()
	var writeDone chan struct{}
	var writeErr error
	var barrier chan struct{}
	defer func() {
		u.mu.Lock()
		u.started = false
		u.mu.Unlock()
		if barrier != nil {
			close(barrier)
		}
		// Any flush that observed started=true sent its event under u.mu.
		select {
		case event := <-u.events:
			if event.barrier != nil {
				close(event.barrier)
			}
		default:
		}
		for _, done := range []<-chan struct{}{decisionDone, writeDone} {
			if done != nil {
				select {
				case <-done:
				case <-ctx.Done():
					t.Error("retention operation did not finish", ctx.Err())
				}
			}
		}
	}()
	select {
	case event := <-u.events:
		if event.barrier == nil {
			t.Fatal("usage event did not contain a flush barrier")
		}
		barrier = event.barrier
	case <-decisionDone:
		t.Fatal("retention decision ended before the usage flush", decisionErr)
	case <-ctx.Done():
		t.Fatal("retention decision did not reach the usage flush", ctx.Err())
	}
	// The decision has consumed its policy snapshot. The writer probes the
	// same exclusion before entering its real operation.
	writeDone = make(chan struct{})
	attempted := make(chan bool, 1)
	go func() {
		acquired := s.Store.retentionRunMu.TryLock()
		if acquired {
			s.Store.retentionRunMu.Unlock()
		}
		attempted <- acquired
		writeErr = write(ctx)
		close(writeDone)
	}()
	select {
	case acquired := <-attempted:
		if acquired {
			select {
			case <-writeDone:
				t.Fatal("competing write advanced before the decision", writeErr)
			case <-ctx.Done():
				t.Fatal("competing write did not finish", ctx.Err())
			}
		}
	case <-ctx.Done():
		t.Fatal("competing write did not start", ctx.Err())
	}
	select {
	case <-writeDone:
		t.Fatal("write completed before the retention decision", writeErr)
	default:
	}
	close(barrier)
	barrier = nil
	for _, done := range []<-chan struct{}{decisionDone, writeDone} {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("retention operation did not finish", ctx.Err())
		}
	}
	if decisionErr != nil || writeErr != nil {
		t.Fatal("retention operation failed", decisionErr, writeErr)
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
				return s.Store.SyncCatalog(context.Background(), testCandidate("20261002000000", CatalogSong{ID: 1, MD5: key}))
			},
		},
		{
			name: "older catalog still commits a mapping",
			prepare: func(t *testing.T, s *Server) {
				if err := s.Store.SyncCatalog(t.Context(), testCandidate("20261002000000", CatalogSong{ID: 1})); err != nil {
					t.Fatal(err)
				}
			},
			write: func(s *Server) error {
				return s.Store.SyncCatalog(context.Background(), testCandidate("20261001000000", CatalogSong{ID: 2, MD5: key}))
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
			write:     func(s *Server) error { return s.Store.SetSongPolicy(context.Background(), 1, SongFavorite) },
			wantQueue: true,
		},
		{
			name: "recorded existing mapping",
			prepare: func(t *testing.T, s *Server) {
				if _, err := s.Store.db.Exec("INSERT INTO media(md5) VALUES (?)", key); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Store.db.Exec("INSERT INTO songs(id) VALUES (1)"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Store.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (1,?)", key); err != nil {
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
				return s.Store.SyncCatalog(context.Background(), testCandidate("20261002000000", CatalogSong{ID: 1, MD5: key}))
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
			write: func(s *Server) error { return s.Store.SetSongPolicy(context.Background(), 1, SongFavorite) },
		},
		{
			name: "recorded mapping",
			prepare: func(t *testing.T, s *Server) {
				if _, err := s.Store.db.Exec("INSERT INTO media(md5) VALUES (?)", key); err != nil {
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
			if _, err := s.Store.db.Exec("CREATE TRIGGER reject_write BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'injected failure'); END"); err != nil {
				t.Fatal(err)
			}
			changed := s.QueuePreparationChanged()
			if err := tc.write(s); err == nil {
				t.Fatal("injected write failure was ignored")
			}
			requireRetentionWake(t, s, false)
			requireQueueWake(t, changed, false)
			var count int
			if err := s.Store.db.QueryRow("SELECT COUNT(*) FROM songs WHERE id=1").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed transaction left a placeholder", count, err)
			}
		})
	}
}

func TestPreferenceCommitHoldsRetentionExclusionThroughNotification(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := s.Store.db.Conn(ctx)
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
	before := s.Store.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- s.Store.SetSongPolicy(ctx, 1, SongBlocked) }()
	waitForDatabaseWait(t, s.Store.db, before)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// The queued writer owns the only connection and commits before this read.
	var policy SongPolicy
	if err := s.Store.db.QueryRowContext(ctx, "SELECT policy FROM song_preferences WHERE song_id=1").Scan(&policy); err != nil || policy != SongBlocked {
		t.Fatal("preference did not commit", policy, err)
	}
	if s.Store.retentionRunMu.TryLock() {
		s.Store.retentionRunMu.Unlock()
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
	if _, err := s.Store.db.Exec("PRAGMA query_only=1"); err != nil {
		t.Fatal(err)
	}
	conn, err := s.Store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := s.Store.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- s.Store.SetSongPolicy(ctx, 1, SongBlocked) }()
	waitForDatabaseWait(t, s.Store.db, before)
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
	returned, err := s.Store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := returned.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Store.retentionRunMu.TryLock() {
		s.Store.retentionRunMu.Unlock()
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

func TestRetentionDecisionExcludesMappingAfterPolicyRead(t *testing.T) {
	s := retentionChangeEngine(t)
	const key = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := s.Store.SetSongPolicy(t.Context(), 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), key+".mp4")
	putRetained(t, path, 12)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.CacheUnlimited, s.cfg.VideoBudgetBytes = false, 0
	s.retained = map[string]retainedVideo{key: {path: path, key: key, size: info.Size(), modified: info.ModTime().UnixNano()}}
	s.retainedBytes = info.Size()
	requireDecisionBeforeWrite(t, s, func(context.Context) error {
		s.runRetention(false)
		return nil
	}, func(ctx context.Context) error {
		if err := s.Store.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"}); err != nil {
			return err
		}
		s.retentionMu.Lock()
		_, retained := s.retained[key]
		s.retentionMu.Unlock()
		if retained {
			return errors.New("mapping write completed before retention applied its decision")
		}
		return nil
	})
	expectRetained(t, path, false)
	assertSongResource(t, s, "1", key)
}

func TestPublicationDecisionExcludesPreferenceAfterPolicyRead(t *testing.T) {
	s := retentionChangeEngine(t)
	const key = "cccccccccccccccccccccccccccccccc"
	if err := s.Store.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"}); err != nil {
		t.Fatal(err)
	}
	s.cfg.CacheUnlimited, s.cfg.VideoBudgetBytes = false, 24
	// Force budget competition so this decision reaches the usage-flush
	// barrier; spare-budget admission deliberately skips ranking.
	const occupant = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	s.retained = map[string]retainedVideo{occupant: {key: occupant, size: 24}}
	s.retainedBytes = 24
	var reservation *publicationReservation
	defer func() {
		if reservation != nil {
			s.releasePublication(reservation)
		}
	}()
	requireDecisionBeforeWrite(t, s, func(ctx context.Context) error {
		var err error
		reservation, err = s.reservePublication(ctx, video{key: key, size: 12})
		return err
	}, func(ctx context.Context) error {
		if err := s.Store.SetSongPolicy(ctx, 1, SongBlocked); err != nil {
			return err
		}
		s.retentionMu.Lock()
		_, published := s.publications[key]
		s.retentionMu.Unlock()
		if !published {
			return errors.New("preference write completed before publication was admitted")
		}
		return nil
	})
}

func TestMappingWriteSerializesWithRetention(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.Store.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	requireRetentionWake(t, s, true)
	path := filepath.Join(t.TempDir(), key+".mp4")
	putRetained(t, path, 12)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.CacheUnlimited, s.cfg.VideoBudgetBytes = false, 12
	const ordinary = "dddddddddddddddddddddddddddddddd"
	seedPrioritySong(t, s, "9000001", ordinary)
	if err := s.Store.usage.write([]usageEvent{{songID: "9000001", at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	ordinaryPath := filepath.Join(filepath.Dir(path), ordinary+".mp4")
	putRetained(t, ordinaryPath, 12)
	ordinaryInfo, err := os.Stat(ordinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	s.retained = map[string]retainedVideo{
		key:      {path: path, key: key, size: info.Size(), modified: info.ModTime().UnixNano()},
		ordinary: {path: ordinaryPath, key: ordinary, size: ordinaryInfo.Size(), modified: ordinaryInfo.ModTime().UnixNano()},
	}
	s.retainedBytes = info.Size() + ordinaryInfo.Size()

	conn, err := s.Store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := s.Store.db.Stats().WaitCount
	written := make(chan error, 1)
	go func() {
		written <- s.Store.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"})
	}()
	waitForDatabaseWait(t, s.Store.db, before)
	if s.Store.retentionRunMu.TryLock() {
		s.Store.retentionRunMu.Unlock()
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
	expectRetained(t, ordinaryPath, false)
}

func TestPreferenceWriteSerializesWithPublication(t *testing.T) {
	s := retentionChangeEngine(t)
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.Store.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: "https://play.udon.dance/files/1/1-video.mp4?e=" + key + "&s=12"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := s.Store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	before := s.Store.db.Stats().WaitCount
	written := make(chan error, 1)
	go func() { written <- s.Store.SetSongPolicy(ctx, 1, SongBlocked) }()
	waitForDatabaseWait(t, s.Store.db, before)
	if s.Store.retentionRunMu.TryLock() {
		s.Store.retentionRunMu.Unlock()
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
