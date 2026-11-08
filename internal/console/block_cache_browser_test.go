package console

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBlockCacheRecoveryBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_STORAGE_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_STORAGE_BROWSER_EVIDENCE for cache recovery browser integration")
	}
	original := testConsole(t)
	cache := t.TempDir()
	next := original.settings
	next.BlockCacheDir = cache
	if err := original.editor.save(next); err != nil {
		t.Fatal(err)
	}
	config := original.configPath
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	c, err := newConsoleWithWorkError(config, "127.0.0.1:0", newFixtureDNS(), nil, errors.New("临时缓存盘不可用"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "block-cache-recovery-live.mjs"), ui.URL, evidence, cache)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	if c.settings.BlockCacheDir != cache || c.editor.savedSettings.BlockCacheDir != "" || c.work != nil {
		t.Fatal("recovery applied before restart or lost the selected default")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("recovery recreated the unavailable directory", err)
	}
}
