package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolvePlaybackRecordsSharedMediaWithoutDownload(t *testing.T) {
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=" + key + "&s=1234"
	var apiCalls, videoCalls atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Api/Songs/play" {
			apiCalls.Add(1)
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusFound)
			return
		}
		videoCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	transport := s.client.Transport
	s.client.Transport = resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = "http", cfg.Origins["play.udon.dance"]
		return transport.RoundTrip(r)
	})
	ctx := context.Background()
	if err := s.SyncCatalog(ctx, testCandidate("20261015000000", CatalogSong{ID: 42, MD5: key}, CatalogSong{ID: 43, MD5: key})); err != nil {
		t.Fatal(err)
	}
	var size sql.NullInt64
	if err := s.usage.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", key).Scan(&size); err != nil || size.Valid {
		t.Fatalf("unknown size must be NULL: %+v, %v", size, err)
	}
	for _, id := range []string{"42", "43"} {
		if _, err := s.resolvePlaybackVideo(ctx, url.Values{"id": {id}, "node": {"cf"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.usage.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", key).Scan(&size); err != nil || !size.Valid || size.Int64 != 1234 {
			t.Fatalf("resolution did not fill size immediately: %+v, %v", size, err)
		}
		assertSongResource(t, s, id, key)
	}
	for table, want := range map[string]int{"media": 1, "song_urls": 2, "song_media": 2, "song_usage": 0, "request_events": 0} {
		var count int
		if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d, want %d: %v", table, count, want, err)
		}
	}
	if apiCalls.Load() != 2 || videoCalls.Load() != 0 {
		t.Fatalf("API calls=%d, video calls=%d", apiCalls.Load(), videoCalls.Load())
	}
	for _, dir := range []string{cfg.videosDir(), cfg.tempDir()} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("resolution created media files: %s: %v %v", dir, entries, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var path string
	if err := reopened.usage.db.QueryRow("SELECT byte_size,source_path FROM media WHERE md5=?", key).Scan(&size, &path); err != nil || !size.Valid || size.Int64 != 1234 || path != "/files/1/42-video.mp4" {
		t.Fatalf("metadata did not survive restart: %+v %q %v", size, path, err)
	}
}

func TestSongURLMetadataIgnoresOlderAndTiedObservations(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute)
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	o := SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "cf",
		URL: "https://play.udon.dance/files/1/42-new.mp4?e=" + key + "&s=24",
		MD5: other, Size: 999, // Untrusted caller fields must not override URL metadata.
		QueryStartedAt: at, ObservedAt: at.Add(time.Second)}
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	older := o
	older.QueryStartedAt = at.Add(-time.Second)
	older.ObservedAt = at.Add(2 * time.Second)
	older.URL = "https://play.udon.dance/files/1/42-old.mp4?e=" + key + "&s=12"
	tied := o
	tied.URL = "https://play.udon.dance/files/1/42-other.mp4?e=" + other + "&s=48"
	for _, stale := range []SongURL{older, tied} {
		if err := s.ObserveSongURL(ctx, stale); err != nil {
			t.Fatal(err)
		}
	}
	var size int64
	var path string
	if err := s.usage.db.QueryRow("SELECT byte_size,source_path FROM media WHERE md5=?", key).Scan(&size, &path); err != nil || size != 24 || path != "/files/1/42-new.mp4" {
		t.Fatal(size, path, err)
	}
	for table, want := range map[string]int{"media": 1, "song_urls": 1, "songs": 0, "song_media": 0} {
		var count int
		if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, err)
		}
	}
	got, err := s.SongURLs(ctx, 42, "cf")
	if err != nil || len(got) != 1 || got[0].URL != o.URL {
		t.Fatal(got, err)
	}
}

func TestSongURLMetadataAndObservationRollbackTogether(t *testing.T) {
	for _, table := range []string{"media", "song_urls"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%v", table, existing), func(t *testing.T) {
				s, _ := setup(t, nil)
				ctx := context.Background()
				at := time.Now().Add(-time.Minute)
				const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				o := SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "cf",
					URL:            "https://play.udon.dance/files/1/42-old.mp4?e=" + key + "&s=12",
					QueryStartedAt: at, ObservedAt: at}
				if existing {
					if err := s.ObserveSongURL(ctx, o); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.usage.db.Exec("CREATE TRIGGER fail_metadata BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'injected storage failure'); END"); err != nil {
					t.Fatal(err)
				}
				next := o
				next.URL = strings.ReplaceAll(strings.ReplaceAll(o.URL, "old.mp4", "new.mp4"), "s=12", "s=24")
				next.QueryStartedAt, next.ObservedAt = at.Add(time.Second), at.Add(time.Second)
				if err := s.ObserveSongURL(ctx, next); !errors.Is(err, ErrLocalStorage) || !errors.Is(s.StorageError(), ErrLocalStorage) {
					t.Fatalf("storage failure not retained: %v", err)
				}
				if existing {
					var size int64
					var path string
					if err := s.usage.db.QueryRow("SELECT byte_size,source_path FROM media WHERE md5=?", key).Scan(&size, &path); err != nil || size != 12 || path != "/files/1/42-old.mp4" {
						t.Fatal(size, path, err)
					}
					got, err := s.SongURLs(ctx, 42, "cf")
					if err != nil || len(got) != 1 || got[0].URL != o.URL || !got[0].QueryStartedAt.Equal(o.QueryStartedAt) {
						t.Fatal(got, err)
					}
				} else {
					for _, name := range []string{"media", "song_urls"} {
						var count int
						if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM " + name).Scan(&count); err != nil || count != 0 {
							t.Fatal(name, count, err)
						}
					}
				}
			})
		}
	}
}

func TestSongURLInvalidMetadataDoesNotWrite(t *testing.T) {
	for _, query := range []string{"", "?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=0", "?e=bad&s=12", "?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=12&s=13"} {
		t.Run(query, func(t *testing.T) {
			s, _ := setup(t, nil)
			at := time.Now()
			err := s.ObserveSongURL(context.Background(), SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "cf",
				URL: "https://play.udon.dance/files/1/42-video.mp4" + query, QueryStartedAt: at, ObservedAt: at})
			if err == nil || s.StorageError() != nil {
				t.Fatal("invalid URL must fail validation without a storage fault", err, s.StorageError())
			}
			for _, table := range []string{"media", "song_urls"} {
				var count int
				if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal(table, count, err)
				}
			}
		})
	}
}

func TestSongURLCanceledTransactionDoesNotDisableColdLoads(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, payload) })
	// Reacquiring the only connection waits for database/sql's cancellation
	// worker to finish rollback, deterministically putting it ahead of Commit.
	s.usage.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := s.usage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO media(md5) VALUES (?)", strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	conn, err := s.usage.db.Conn(waitCtx)
	if err != nil {
		t.Fatal("automatic rollback did not release the connection", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	commitErr := tx.Commit()
	if !errors.Is(commitErr, sql.ErrTxDone) {
		t.Fatalf("did not reproduce rollback before Commit: %v", commitErr)
	}
	if err := s.songURLStorageError(ctx, commitErr); !errors.Is(err, context.Canceled) || s.StorageError() != nil {
		t.Fatalf("cancellation poisoned storage: returned=%v retained=%v", err, s.StorageError())
	}
	var count int
	if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM media").Scan(&count); err != nil || count != 0 {
		t.Fatal("canceled transaction was not rolled back", count, err)
	}
	now := time.Now()
	if err := s.ObserveSongURL(context.Background(), SongURL{SongID: 42, URL: videoURL(payload),
		API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: now, ObservedAt: now}); err != nil {
		t.Fatal("subsequent observation failed", err)
	}
	if source, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil || source != "MISS" {
		t.Fatal("cancellation blocked a subsequent cold load", source, err)
	}
}

func TestSongURLTransactionErrorClassification(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	diskError := errors.New("disk write failure")
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		err     error
		want    error
		storage bool
	}{
		{"active transaction ended", context.Background(), sql.ErrTxDone, sql.ErrTxDone, true},
		{"cancellation and disk error", canceled, diskError, diskError, true},
		{"deadline rollback", expired, sql.ErrTxDone, context.DeadlineExceeded, false},
		{"wrapped rollback", canceled, fmt.Errorf("commit: %w", sql.ErrTxDone), context.Canceled, false},
		{"success before cancellation", canceled, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := new(Server)
			got := s.songURLStorageError(tc.ctx, tc.err)
			if !errors.Is(got, tc.want) || (s.StorageError() != nil) != tc.storage || errors.Is(got, ErrLocalStorage) != tc.storage {
				t.Fatalf("returned=%v retained=%v, want=%v storage=%v", got, s.StorageError(), tc.want, tc.storage)
			}
		})
	}
}
