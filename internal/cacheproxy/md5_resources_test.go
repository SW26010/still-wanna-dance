package cacheproxy

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMD5PlaybackCheckRedactsTransportURL(t *testing.T) {
	s, _ := setup(t, nil)
	var logs bytes.Buffer
	s.cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	cause := errors.New("connection refused")
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
		return "", &url.Error{Op: "Get", URL: "https://example.test/private?token=secret-value", Err: cause}
	}
	local := parsedVideo(t, s, payload)
	local.songID = "42"
	check := s.startPlaybackCheck(url.Values{"id": {"42"}}, local)
	select {
	case <-check.done:
	case <-time.After(5 * time.Second):
		t.Fatal("check stalled")
	}
	s.wg.Wait()
	if !errors.Is(check.err, cause) {
		t.Fatalf("error identity lost: %v", check.err)
	}
	got := logs.String()
	if !strings.Contains(got, "playback_check_failed") || !strings.Contains(got, "connection refused") || !strings.Contains(got, `"song_id":"42"`) {
		t.Fatal(got)
	}
	for _, secret := range []string{"example.test", "/private", "token", "secret-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("URL leaked: %s", got)
		}
	}
}

func TestMD5IdentityAcrossDifferentResourceVersions(t *testing.T) {
	var downloads atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); io.WriteString(w, payload) })
	first := videoURL(payload)
	second := strings.Replace(strings.Replace(first, "1344-660524b4ebadb", "9876-entirelyDifferent", 1), "/2403/", "/9999/", 1)
	for i, target := range []string{first, second} {
		if _, err := s.PrefetchSong(context.Background(), fmt.Sprint(i+1), target); err != nil {
			t.Fatal(err)
		}
	}
	key := parsedVideo(t, s, payload).key
	files, err := os.ReadDir(cfg.videosDir())
	if err != nil || len(files) != 1 || files[0].Name() != key+".mp4" || downloads.Load() != 1 {
		t.Fatalf("files=%v downloads=%d err=%v", files, downloads.Load(), err)
	}
	for _, id := range []string{"1", "2"} {
		assertSongResource(t, s, id, key)
	}
}

func TestMD5ReferenceSnapshotCatalogUpdatesAndMissing(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	a, b, orphan := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	if err := syncTestCatalog(s, ctx, map[string]string{"1": a, "2": a, "3": b}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{a, orphan} {
		if err := os.WriteFile(cfg.videoFile(key), []byte("trusted content; no hashing"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, release, err := s.CheckReferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || len(r.Present) != 1 || len(r.Missing) != 1 || len(r.Missing[b]) != 1 || r.Present[orphan] {
		t.Fatalf("%+v", r)
	}
	release()
	if err := syncTestCatalog(s, ctx, map[string]string{"2": b, "4": a}); err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "1", a)
	assertSongResource(t, s, "2", b)
	assertSongResource(t, s, "3", b)
	assertSongResource(t, s, "4", a)
	r, release, err = s.CheckReferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(r.References) != 4 || len(r.Missing[b]) != 2 {
		t.Fatalf("%+v", r)
	}
	if err := syncTestCatalog(s, ctx, map[string]string{"5": "invalid"}); err == nil {
		t.Fatal("invalid mapping accepted")
	}
}

func TestMD5PlaybackImmediatelyReturnsAndMismatchOnlyLogs(t *testing.T) {
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); io.WriteString(w, payload) })
	ctx := context.Background()
	key := parsedVideo(t, s, payload).key
	if _, err := s.PrefetchSong(ctx, "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	s.cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
		close(started)
		select {
		case <-finish:
			return videoURL("new different data"), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
		if w.Code != 200 || w.Body.String() != payload {
			t.Errorf("%d %q", w.Code, w.Body.String())
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("local playback waited for upstream")
	}
	<-started
	close(finish)
	s.wg.Wait()
	assertSongResource(t, s, "42", key)
	if downloads.Load() != 1 {
		t.Fatal("mismatch triggered a refresh download")
	}
}

func TestMD5UnknownIDColdLoadRecordsMappingAndCatalogWins(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(payload), nil }
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil), 200, payload)
	key := parsedVideo(t, s, payload).key
	assertSongResource(t, s, "42", key)
	current := strings.Repeat("d", 32)
	if err := syncTestCatalog(s, context.Background(), map[string]string{"42": current}); err != nil {
		t.Fatal(err)
	}
	// A missing authoritative file may cold-load the available URL, but not roll back the catalog.
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil), 200, payload)
	assertSongResource(t, s, "42", current)
}

func TestMD5PublishedFilesTrustedAcrossRestart(t *testing.T) {
	var downloads atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); io.WriteString(w, payload) })
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	key := parsedVideo(t, s, payload).key
	s.Close()
	// Deliberate external modification demonstrates the chosen immutable-file contract.
	if err := os.WriteFile(cfg.videoFile(key), []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	assertResponse(t, request(next, "GET", videoURL(payload), nil), 200, "trusted")
	if downloads.Load() != 1 {
		t.Fatal(downloads.Load())
	}
}

func TestMD5ExpectedContentRejectsStaleURLBeforeDownload(t *testing.T) {
	var downloads atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { downloads.Add(1) })
	_, err := s.PrefetchSong(WithExpectedMD5(context.Background(), strings.Repeat("a", 32)), "1", videoURL(payload))
	if err == nil || downloads.Load() != 0 {
		t.Fatal(err, downloads.Load())
	}
}

func TestMD5RetentionSumsSongsAndOrphansHaveZeroScore(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	a, b, c := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	if err := syncTestCatalog(s, ctx, map[string]string{"1": a, "2": a, "3": b}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{a, b, c} {
		putRetained(t, cfg.videoFile(key), 10)
	}
	now := time.Now().UnixMilli()
	for _, e := range []struct {
		id    string
		score float64
	}{{"1", 2}, {"2", 2}, {"3", 3}} {
		_, err := s.usage.db.Exec("INSERT INTO song_usage VALUES (?,1,?,?)", e.id, e.score, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	setRetentionLimit(t, s, 10)
	s.trimCache()
	expectRetained(t, cfg.videoFile(a), true)
	expectRetained(t, cfg.videoFile(b), false)
	expectRetained(t, cfg.videoFile(c), false)
}

func TestMD5ReferenceSnapshotProtectsFilesDuringUse(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("a", 32)
	if err := syncTestCatalog(s, context.Background(), map[string]string{"1": key}); err != nil {
		t.Fatal(err)
	}
	putRetained(t, cfg.videoFile(key), 10)
	r, release, err := s.CheckReferences(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete {
		release()
		t.Fatal(r)
	}
	setRetentionLimit(t, s, 1)
	s.trimCache()
	expectRetained(t, cfg.videoFile(key), true)
	release()
	s.trimCache()
	expectRetained(t, cfg.videoFile(key), false)
}

func TestMD5OldStorageRejectedWithoutMigration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "storage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE video_versions(version_key TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cfg := fixtureConfig()
	cfg.StorageDir = root
	if s, err := New(cfg); err == nil {
		s.Close()
		t.Fatal("old storage accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatal(version, err)
	}
}

func BenchmarkMD5ReferenceCheck(b *testing.B) {
	root := b.TempDir()
	os.MkdirAll(filepath.Join(root, "videos"), 0700)
	refs := map[string]string{}
	for i := 0; i < 10000; i++ {
		refs[fmt.Sprint(i)] = fmt.Sprintf("%032x", i/2)
	}
	for i := 0; i < 4000; i++ {
		if err := os.WriteFile(filepath.Join(root, "videos", fmt.Sprintf("%032x.mp4", i)), nil, 0600); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := CheckReferencedFiles(context.Background(), root, refs); err != nil {
			b.Fatal(err)
		}
	}
}
