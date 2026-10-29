package console

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestImportSpaceUsesActiveOrBatchDestinationWithoutStartingWork(t *testing.T) {
	c := testConsole(t)
	source := t.TempDir()
	c.savedSettings.VideoDir = filepath.Join(t.TempDir(), "not-yet-active")
	check := func(source, wantMode, wantTarget string, status int) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/import/space?"+url.Values{"source": {source}}.Encode(), nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatal(w.Code, w.Body.String())
		}
		if status != 200 {
			return
		}
		var result struct{ Mode, VideoDir string }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Mode != wantMode || result.VideoDir != wantTarget {
			t.Fatal(result, err)
		}
	}
	check(source, "same-volume", c.settings.VideoDir, 200)
	check(filepath.Join(source, "missing"), "unknown", c.settings.VideoDir, 200)
	for _, invalid := range []string{"", "relative", "bad\x00path"} {
		check(invalid, "", "", 400)
	}
	c.importStatus = ImportStatus{Present: true, Source: source, VideoDir: t.TempDir()}
	check(source, "same-volume", c.importStatus.VideoDir, 200)
	check(t.TempDir(), "same-volume", c.settings.VideoDir, 200)
	if c.service != nil || c.importStatus.Running || c.importTask != nil {
		t.Fatal("estimate started an engine or import")
	}
	if _, err := os.Stat(c.savedSettings.VideoDir); !os.IsNotExist(err) {
		t.Fatal("estimate created an inactive destination", err)
	}
	c.terms = termsReceipt{}
	check(source, "", "", 428)
}

func TestImportSpaceBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_IMPORT_SPACE_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_IMPORT_SPACE_BROWSER_EVIDENCE for real Console integration")
	}
	c := testConsole(t)
	source := t.TempDir()
	c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return source, nil }
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "import-space-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if c.service != nil || c.importStatus.Present {
		t.Fatal("space preview started work")
	}
}
