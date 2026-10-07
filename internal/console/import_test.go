package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
