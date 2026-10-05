package console

import (
	"context"
	"database/sql"
	"fmt"
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
		if name != "latest name" || artist != "artist" || md5 != b {
			t.Fatalf("%q %q %q", name, artist, md5)
		}
		var count int
		if err := db.QueryRow("SELECT count(*) FROM songs").Scan(&count); err != nil || count != 2 {
			t.Fatalf("songs=%d, err=%v", count, err)
		}
		var revision string
		if err := db.QueryRow("SELECT revision FROM catalog_state WHERE catalog_key='songs'").Scan(&revision); err != nil || revision != newerRevision {
			t.Fatalf("full revision=%s, err=%v", revision, err)
		}
	}
	assert()
	// Old or conflicting candidates cannot overwrite accepted data or delete ID 2.
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
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorNames("20261001000000", "original")})
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
		c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorNames("20261002000000", "replacement")})
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
		c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorNames("20261003000000", "name")})
		cancel()
		if c.service != nil {
			t.Fatal("opened storage for canceled/closing update")
		}
		c.closing = false
	}
}
