package console

import (
	"context"
	"crypto/md5"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestImportCleanupRetainsOnlyFailedReceiptsForConfirmedRetry(t *testing.T) {
	c := testConsole(t)
	c.settings.VideoStorageMode = "unlimited" // Source cleanup requires retained destination videos.
	defer c.Close()
	c.lifecycleMu.Lock()
	_, err := c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var receipts []cacheproxy.ImportedVideo
	for _, body := range []string{"first video", "second video"} {
		key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
		source := filepath.Join(root, key+".mp4")
		if err := os.WriteFile(source, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		receipt, err := c.service.ImportVideo(context.Background(), source, map[string]bool{key: true})
		if err != nil || receipt == nil {
			t.Fatal(receipt, err)
		}
		receipts = append(receipts, *receipt)
	}
	c.importReceipts = receipts
	c.importStatus = ImportStatus{ID: 1, Imported: 2, CleanupAvailable: true}
	// A temporarily unavailable destination must preserve its source and receipt.
	missing := receipts[1].Destination
	if err := os.Rename(missing, missing+".held"); err != nil {
		t.Fatal(err)
	}
	waitCleanup := func() {
		t.Helper()
		if err := c.cleanupImport(1, true); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		done := c.importDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup stalled")
		}
	}
	waitCleanup()
	if len(c.importReceipts) != 1 || c.importReceipts[0].Source != receipts[1].Source || !c.importStatus.CleanupAvailable || c.importStatus.Deleted != 1 || c.importStatus.Failed != 1 {
		t.Fatalf("failed receipt not retained: %+v, %+v", c.importStatus, c.importReceipts)
	}
	if _, err := os.Stat(receipts[0].Source); !os.IsNotExist(err) {
		t.Fatal("successful source remains", err)
	}
	if _, err := os.Stat(receipts[1].Source); err != nil {
		t.Fatal("failed source lost", err)
	}
	if err := c.cleanupImport(1, false); err == nil {
		t.Fatal("retry bypassed confirmation")
	}
	if err := os.Rename(missing+".held", missing); err != nil {
		t.Fatal(err)
	}
	waitCleanup()
	if len(c.importReceipts) != 0 || c.importStatus.CleanupAvailable || c.importStatus.Deleted != 2 || c.importStatus.Total != 1 || c.importStatus.Checked != 1 || c.importStatus.Failed != 0 {
		t.Fatalf("retry did not finish: %+v", c.importStatus)
	}
	if _, err := os.Stat(receipts[1].Source); !os.IsNotExist(err) {
		t.Fatal("retried source remains", err)
	}
}

func TestImportUsesAllKnownMediaWithoutNetworkOrMapping(t *testing.T) {
	c := testConsole(t)
	c.settings.VideoStorageMode = "unlimited"
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Error("import contacted network")
		return nil, errors.New("offline")
	})
	c.checksumURL = cacheproxy.UdonCatalogURL
	digest := func(body string) string { return fmt.Sprintf("%x", md5.Sum([]byte(body))) }
	// Accumulated metadata retains the historical song, then accepts a new member.
	for i, body := range []string{"historical", "current"} {
		if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: fmt.Sprintf("2026100%d000000", i+1), Source: cacheproxy.KivaCatalogURL, Songs: []cacheproxy.CatalogSong{{ID: int64(i + 1), MD5: digest(body)}}}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", c.settings.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media(md5) VALUES (?)", digest("orphan")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	source := t.TempDir()
	for _, body := range []string{"historical", "current", "orphan", "unknown"} {
		if err := os.WriteFile(filepath.Join(source, body+".mp4"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.startImport(source); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		done := c.importDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("import stalled")
		}
		state := c.importStatus
		wantImported, wantSkipped := 3, 1
		if attempt == 1 {
			wantImported, wantSkipped = 0, 4
		}
		if state.Running || state.Checked != 4 || state.Imported != wantImported || state.Skipped != wantSkipped || state.Failed != 0 || state.Error != "" || state.CatalogWarning != "" {
			t.Fatal(state)
		}
	}
	for _, body := range []string{"historical", "current", "orphan"} {
		if got, err := os.ReadFile(filepath.Join(c.settings.VideoDir, digest(body)+".mp4")); err != nil || string(got) != body {
			t.Fatal(body, string(got), err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.settings.VideoDir, digest("unknown")+".mp4")); !os.IsNotExist(err) {
		t.Fatal("unknown media admitted", err)
	}
}

func TestImportMediaOnlyAndCancellation(t *testing.T) {
	for _, kind := range []string{"media-only", "empty", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			c := testConsole(t)
			c.settings.VideoStorageMode = "unlimited"
			c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
				t.Error("import contacted network")
				return nil, errors.New("offline")
			})
			body := "known media without a song"
			key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
			if kind != "empty" {
				db, err := sql.Open("sqlite", c.settings.DatabasePath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("INSERT INTO media(md5) VALUES (?)", key)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			c.lifecycleMu.Lock()
			engine, err := c.ensureEngineLocked()
			c.lifecycleMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "video.mp4"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			c.runImport(ctx, engine, source, c.settings.VideoDir, make(chan struct{}))
			state := c.importStatus
			switch kind {
			case "media-only":
				if state.Imported != 1 || state.Error != "" {
					t.Fatal(state)
				}
			case "empty":
				if state.Checked != 0 || !strings.Contains(state.Error, "本地尚无已知资源") {
					t.Fatal(state)
				}
			case "canceled":
				if state.Checked != 0 || state.Error != context.Canceled.Error() {
					t.Fatal(state)
				}
			}
			status, err := c.store.CatalogStatus(t.Context())
			if err != nil || status.Revision != "" {
				t.Fatal("import required or created a song watermark", status, err)
			}
		})
	}
}

func TestImportEnumerationFreezesPathsAndExcludesStorage(t *testing.T) {
	root := t.TempDir()
	storage := filepath.Join(root, "own")
	nested := filepath.Join(root, "nested")
	for _, dir := range []string{storage, nested} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(root, "first.MP4"), filepath.Join(nested, "second.mkv"), filepath.Join(root, "text.txt"), filepath.Join(storage, "own.mp4")} {
		if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := enumerateImport(context.Background(), root, storage)
	if err != nil || len(files) != 2 {
		t.Fatal(files, err)
	}
	old := files[0]
	if err := os.Rename(old, old+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "later.mp4"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != old {
		t.Fatal("snapshot changed", files)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("snapshot followed renamed source")
	}
}

func TestImportCleanupRequiresExplicitConfirmationAndCurrentJob(t *testing.T) {
	c := &Console{importStatus: ImportStatus{ID: 2, CleanupAvailable: true}}
	if err := c.cleanupImport(2, false); err == nil {
		t.Fatal("confirmation missing")
	}
	if err := c.cleanupImport(1, true); err == nil {
		t.Fatal("stale job allowed")
	}
}
