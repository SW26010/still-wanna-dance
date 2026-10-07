package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestImportCleanupRetainsOnlyFailedReceiptsForConfirmedRetry(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	c.mu.Lock()
	err := c.ensureEngine()
	c.mu.Unlock()
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

func TestImportRefreshAndProgress(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "refresh_failure"}[unavailable], func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if unavailable {
					http.Error(w, "unavailable", 503)
					return
				}
				writeTestMD5Catalog(w, map[int]string{138: "matched video"})
			}))
			defer api.Close()
			c.checksumURL = api.URL + "/catalog"
			c.client.Transport = http.DefaultTransport
			source := t.TempDir()
			for name, body := range map[string]string{"match.mp4": "matched video", "other.mkv": "unknown video"} {
				if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
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
			if unavailable {
				if state.Error == "" || state.Checked != 0 || state.Imported != 0 {
					t.Fatalf("import used stale catalog: %+v", state)
				}
			} else if state.Running || state.Total != 2 || state.Checked != 2 || state.Imported != 1 || state.Skipped != 1 || state.Failed != 0 || !state.CleanupAvailable {
				t.Fatalf("unexpected import: %+v", state)
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
