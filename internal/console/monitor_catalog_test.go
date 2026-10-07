package console

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/upstreamstate"
)

func monitorFull(route, revision, name, md5 string) upstreamstate.CatalogResponse {
	return upstreamstate.CatalogResponse{Route: route, Source: "https://" + route + ".example/catalog", Body: []byte(fmt.Sprintf(`{"code":200,"data":{"time":%q,"groups":[{"entries":[{"id":1,"name":%q,"artist":"artist","checksum":%q}]}]}}`, revision, name, md5))}
}

func monitorNames(revision, name string) upstreamstate.CatalogResponse {
	return upstreamstate.CatalogResponse{Route: "api", Source: "https://api.udon.dance/Api/Songs/list", Body: []byte(fmt.Sprintf(`{"time":%q,"groups":{"contents":[{"songInfos":[{"id":1,"name":%q},{"id":2,"name":"new song"}]}]}}`, revision, name))}
}

func TestMonitorCatalogOrderingAndExistingWatermarks(t *testing.T) {
	c := testConsole(t)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	old := monitorFull("wanna", "20261001000000", "old", a)
	newer := monitorFull("kiva", "20261002000000", "full", b)
	// Udon arrives first, but must not block this batch's complete metadata.
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorNames("20261003000000", "latest name"), old, newer, newer})
	db, err := sql.Open("sqlite", filepath.Join(c.settings.StorageDir, "stepstash.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assert := func() {
		t.Helper()
		var name, artist, md5 string
		if err := db.QueryRow("SELECT name,artist,md5 FROM songs JOIN song_media USING(song_id) WHERE song_id=1").Scan(&name, &artist, &md5); err != nil {
			t.Fatal(err)
		}
		if name != "full" || artist != "artist" || md5 != b {
			t.Fatalf("%q %q %q", name, artist, md5)
		}
		var count int
		if err := db.QueryRow("SELECT count(*) FROM songs").Scan(&count); err != nil || count != 1 {
			t.Fatalf("songs=%d, err=%v", count, err)
		}
		var revision string
		if err := db.QueryRow("SELECT revision FROM catalog_state WHERE catalog_key='songs'").Scan(&revision); err != nil || revision != newerRevision {
			t.Fatalf("full revision=%s, err=%v", revision, err)
		}
	}
	assert()
	// Udon cannot add ID 2 or overwrite MD5 metadata, regardless of its time.
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{old, monitorNames("20261003000000", "conflict"), monitorFull("wanna", "20261002000000", "full", a)})
	assert()
	// Fully invalid newer data must not advance any watermark.
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorFull("kiva", "20261004000000", "invalid", "bad")})
	assert()
}

const newerRevision = "20261002000000"

func TestMonitorCatalogUsesCurrentStorageAfterSwitch(t *testing.T) {
	c := testConsole(t)
	ctx := context.Background()
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorFull("kiva", "20261001000000", "original", strings.Repeat("a", 32))})
	oldRoot := c.settings.StorageDir
	// Hold the same locks as settings changes while replacing the engine.
	c.lifecycleMu.Lock()
	c.mu.Lock()
	old := c.service
	c.service = nil
	c.settings.StorageDir = filepath.Join(t.TempDir(), "replacement")
	c.mu.Unlock()
	if err := old.Close(); err != nil {
		c.lifecycleMu.Unlock()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorFull("wanna", "20261002000000", "replacement", strings.Repeat("b", 32))})
	}()
	c.lifecycleMu.Unlock()
	<-done
	for root, want := range map[string]string{oldRoot: "original", c.settings.StorageDir: "replacement"} {
		db, err := sql.Open("sqlite", filepath.Join(root, "stepstash.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		var got string
		err = db.QueryRow("SELECT name FROM songs WHERE song_id=1").Scan(&got)
		db.Close()
		if err != nil || got != want {
			t.Fatalf("%s: name=%q, error=%v", root, got, err)
		}
	}
}

func TestMonitorCatalogCanceledOrClosingDoesNotOpenStorage(t *testing.T) {
	for _, closing := range []bool{false, true} {
		c := testConsole(t)
		ctx, cancel := context.WithCancel(context.Background())
		if closing {
			c.closing = true
		} else {
			cancel()
		}
		c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorFull("kiva", "20261003000000", "name", strings.Repeat("a", 32))})
		cancel()
		if c.service != nil {
			t.Fatal("opened storage for canceled/closing update")
		}
		c.closing = false
	}
}

func TestMonitorUdonNeverOpensStorage(t *testing.T) {
	c := testConsole(t)
	c.syncMonitorCatalogs(context.Background(), []upstreamstate.CatalogResponse{monitorNames("20990101000000", "ignored")})
	if c.service != nil {
		t.Fatal("Udon opened database")
	}
	if _, err := os.Stat(c.settings.StorageDir); !os.IsNotExist(err) {
		t.Fatal("Udon created storage", err)
	}
}

func TestMonitorCatalogFailureStatus(t *testing.T) {
	c := testConsole(t)
	ctx := context.Background()
	good := monitorFull("kiva", "20261002000000", "good", strings.Repeat("a", 32))
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{good})
	before := c.localInventory().Catalog
	conflict := monitorFull("wanna", "20261002000000", "conflict", strings.Repeat("b", 32))
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{conflict})
	after := c.localInventory().Catalog
	if after.Error == "" || after.Revision != before.Revision || !after.CheckedAt.Equal(before.CheckedAt) {
		t.Fatal(before, after)
	}
	old := monitorFull("wanna", "20261001000000", "old", strings.Repeat("a", 32))
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{conflict, old})
	if c.localInventory().Catalog.Error == "" {
		t.Fatal("old mirror hid conflict")
	}
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{good})
	if c.localInventory().Catalog.Error != "" {
		t.Fatal("successful retry did not clear error")
	}
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorFull("kiva", "20261003000000", "bad", "invalid")})
	if c.localInventory().Catalog.Error == "" {
		t.Fatal("validation error not recorded")
	}
}

func TestInventoryLoadsCatalogStatusWithoutEngine(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	c.syncMonitorCatalogs(context.Background(), []upstreamstate.CatalogResponse{monitorFull("kiva", "20261002000000", "good", strings.Repeat("a", 32))})
	c.syncMonitorCatalogs(context.Background(), []upstreamstate.CatalogResponse{monitorFull("wanna", "20261002000000", "conflict", strings.Repeat("b", 32))})
	want := c.localInventory().Catalog
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	if c.service == nil {
		t.Fatal("save closed active engine")
	}
	if got := c.localInventory().Catalog; got != want {
		t.Fatal(got, want)
	}
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.localInventory().Catalog; got != want {
		t.Fatal(got, want)
	}
	if reopened.service != nil {
		t.Fatal("status read started engine")
	}
	next := c.settings
	next.StorageDir = t.TempDir()
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	c.inventory.Catalog = want // A stale snapshot must never supply current status.
	if got := c.localInventory().Catalog; got.Revision != "" || got.Error != "" || !got.CheckedAt.IsZero() {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(next.StorageDir, "stepstash.sqlite")); !os.IsNotExist(err) {
		t.Fatal("read created database", err)
	}
	if err := os.WriteFile(filepath.Join(next.StorageDir, "stepstash.sqlite"), []byte("invalid database"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := c.localInventory().Catalog; got.Revision != "" || !strings.Contains(got.Error, "无法读取本地清单状态") {
		t.Fatal("database failure reused stale status", got)
	}
}
