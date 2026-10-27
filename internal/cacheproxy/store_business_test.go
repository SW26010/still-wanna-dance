package cacheproxy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStoreBusinessOperationsWithoutVideoEngine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "personal.sqlite")
	store, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	key := strings.Repeat("a", 32)
	if err := store.SyncCatalog(ctx, testCandidate("20261001000000", CatalogSong{ID: 19, MD5: key})); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSongPolicy(ctx, 19, SongFavorite); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Second)
	o := SongURL{SongID: 19, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", URL: "https://play.udon.dance/files/1/19-video.mp4?e=" + key + "&s=12", QueryStartedAt: started, ObservedAt: time.Now()}
	if err := store.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportSongUsage(ctx, historyAt(19, started.UnixMilli()), false); err != nil {
		t.Fatal(err)
	}
	if count, err := store.SongUsageCount(ctx); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if ids, err := store.SongMetadataCandidates(ctx, o.API); err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	var observations int
	if err := store.db.QueryRow("SELECT count(*) FROM request_observation").Scan(&observations); err != nil || observations != 0 {
		t.Fatal("metadata started traffic collection", observations, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.IsDir() {
			t.Fatal("business operation created media directories", f.Name())
		}
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policies, err := store.SongPolicies(ctx)
	if err != nil || policies[19] != SongFavorite {
		t.Fatal(policies, err)
	}
	urls, err := store.SongURLs(ctx, 19, "cf")
	if err != nil || len(urls) != 1 || urls[0].MD5 != key {
		t.Fatal(urls, err)
	}
	status, err := store.CatalogStatus(ctx)
	if err != nil || status.Revision != "20261001000000" || status.CheckedAt.IsZero() {
		t.Fatal(status, err)
	}
}

func TestEngineBorrowsStoreAndCannotCloseItsOwner(t *testing.T) {
	store, err := CreateStore(filepath.Join(t.TempDir(), "personal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := fixtureConfig()
	cfg.Store = store
	prepareFixtureVideo(t, &cfg, t.TempDir())
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := store.Close(); err == nil {
		t.Fatal("closed an active engine's database")
	}
	if err := store.SetSongPolicy(t.Context(), 3, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSongPolicy(t.Context(), 4, SongBlocked); err != nil {
		t.Fatal("engine closed application store", err)
	}
	policies, err := store.SongPolicies(t.Context())
	if err != nil || policies[3] != SongFavorite || policies[4] != SongBlocked {
		t.Fatal(policies, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CatalogStatus(context.Background()); err == nil {
		t.Fatal("accepted work after close")
	}
}

func TestStoreReportsBackgroundWriteFailure(t *testing.T) {
	for name, fault := range map[string]string{
		"readonly":    "PRAGMA query_only=1",
		"transaction": "CREATE TRIGGER reject_usage BEFORE INSERT ON song_usage BEGIN SELECT RAISE(ABORT, 'fixture failure'); END",
	} {
		t.Run(name, func(t *testing.T) {
			store, err := CreateStore(filepath.Join(t.TempDir(), "personal.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.usage.start(0, true); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(fault); err != nil {
				t.Fatal(err)
			}
			store.usage.startGET("3", strings.Repeat("a", 32))
			store.usage.flush()
			if store.Error() == nil {
				t.Fatal("background write failure only went to log")
			}
			select {
			case <-store.failureDone():
			default:
				t.Fatal("waiting work was not notified")
			}
			if err := store.Close(); err == nil {
				t.Fatal("close concealed an uncommitted write failure")
			}
		})
	}
}

func TestStoreCloseDrainsAcceptedBusinessEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal.sqlite")
	store, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	// Hold the sole connection while filling a small, non-overflowing queue.
	// No explicit flush may rescue a Close that shuts the DB before its worker.
	conn, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for id := int64(1); id <= 32; id++ {
		recordDemand(store.usage, strconv.FormatInt(id, 10), time.Now())
	}
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var requests, songs, demands int
	if err := reopened.db.QueryRow("SELECT count(*) FROM request_events").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRow("SELECT count(*),sum(demand_count) FROM song_usage").Scan(&songs, &demands); err != nil {
		t.Fatal(err)
	}
	if requests != 32 || songs != 32 || demands != 32 {
		t.Fatal("Close lost accepted requests or accumulated demand", requests, songs, demands)
	}
}

func TestSecondEngineCannotCleanOwnedWork(t *testing.T) {
	root := t.TempDir()
	store, err := CreateStore(storagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := fixtureConfig()
	cfg.Store = store
	prepareFixtureVideo(t, &cfg, filepath.Join(root, "videos"))
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	part := filepath.Join(cfg.tempDir(), "download-block-123456.part")
	if err := os.WriteFile(part, []byte("active"), 0600); err != nil {
		t.Fatal(err)
	}
	other, err := New(cfg)
	if err == nil {
		other.Close()
		t.Fatal("attached a second engine")
	}
	if body, err := os.ReadFile(part); err != nil || string(body) != "active" {
		t.Fatal("rejected engine cleaned active work", string(body), err)
	}
}

func TestStoreDoesNotReportRejectedInputAsDatabaseDamage(t *testing.T) {
	store, err := CreateStore(filepath.Join(t.TempDir(), "personal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetSongPolicy(t.Context(), -1, SongFavorite); err == nil {
		t.Fatal("accepted invalid song")
	}
	if err := store.ObserveSongURL(t.Context(), SongURL{}); err == nil {
		t.Fatal("accepted invalid observation")
	}
	if err := store.SyncCatalog(t.Context(), Catalog{}); err == nil {
		t.Fatal("accepted invalid catalog")
	}
	if store.Error() != nil {
		t.Fatal("input rejection marked storage damaged", store.Error())
	}
}
