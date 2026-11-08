package console

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestStorageRepairBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_STORAGE_REPAIR_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_STORAGE_REPAIR_BROWSER_EVIDENCE for storage repair integration")
	}
	for _, scenario := range []string{"confirmation", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config.json")
			database := filepath.Join(root, "data", "database", "storage.sqlite")
			videos := filepath.Join(root, "videos")
			createTestDatabase(t, database)
			if err := os.MkdirAll(videos, 0700); err != nil {
				t.Fatal(err)
			}
			video := filepath.Join(videos, strings.Repeat("a", 32)+".mp4")
			if err := os.WriteFile(video, []byte("existing video"), 0600); err != nil {
				t.Fatal(err)
			}
			body := `{"schemaVersion":8,"videoStorageMode":"unlimited","restoreHostsOnStopOrExit":false}`
			if scenario == "failure" {
				body = fmt.Sprintf(`{"schemaVersion":8,"databasePath":%q,"videoDir":%q,"videoStorageMode":"unlimited","restoreHostsOnStopOrExit":false}`, filepath.Join(root, "missing.sqlite"), videos)
			}
			if err := os.WriteFile(config, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(database)
			if err != nil {
				t.Fatal(err)
			}
			c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			c.editor.hosts, _ = fixtureHostsController(t, hostsBaseline)
			c.checksumURL = ""
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
			c.editor.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return database, nil }
			run := func(phase string) {
				t.Helper()
				ui := httptest.NewUnstartedServer(c)
				c.address = ui.Listener.Addr().String()
				ui.Start()
				defer ui.Close()
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "storage-repair-live.mjs"), ui.URL, filepath.Join(evidence, scenario), phase)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("browser: %v\n%s", err, output)
				}
			}
			run(scenario)
			if c.editor.savedSettings.DatabasePath != database || c.editor.savedSettings.VideoDir != videos || c.settings == c.editor.savedSettings || c.service != nil {
				t.Fatal("repair did not remain staged", c.editor.savedSettings.DatabasePath, c.editor.savedSettings.VideoDir)
			}
			if after, err := os.ReadFile(database); err != nil || !bytes.Equal(before, after) {
				t.Fatal("repair changed the unopened existing database", err)
			}
			if after, err := os.ReadFile(video); err != nil || string(after) != "existing video" {
				t.Fatal("repair modified existing video", err)
			}
			c = restartTestConsole(t, c)
			if c.store == nil || c.storageOpenError != nil || c.storageConfirmation != nil {
				t.Fatal("saved repair did not reopen", c.storageOpenError, c.storageConfirmation)
			}
			run("reopened")
		})
	}
}
