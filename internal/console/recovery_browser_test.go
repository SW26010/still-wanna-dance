package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
)

func TestRecoverySharedSettingsBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_RECOVERY_SETTINGS_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_RECOVERY_SETTINGS_BROWSER_EVIDENCE for real shared settings integration")
	}
	view := recoveryFixture(t, `{"schemaVersion":8,"restoreHostsOnStopOrExit":false,"videoStorageMode":"limited","videoBudgetBytes":0}`)
	root := t.TempDir()
	database, video := filepath.Join(root, "new.sqlite"), filepath.Join(root, "videos")
	if err := os.Mkdir(video, 0700); err != nil {
		t.Fatal(err)
	}
	view.console.pathPicker = func(_ context.Context, dialog desktop.PathDialog) (string, error) {
		if dialog.Mode == "database" {
			return database, nil
		}
		return video, nil
	}
	managed, err := transformHosts(hostsBaseline, "enable")
	if err != nil {
		t.Fatal(err)
	}
	view.console.hosts.close(false)
	h, launches := fixtureHostsController(t, managed)
	view.console.hosts = h
	launch, attempts := h.launch, 0
	h.launch = func(ctx context.Context) (hostsConnection, error) {
		attempts++
		if attempts == 1 {
			return nil, errHostsAuthorizationCanceled
		}
		return launch(ctx)
	}
	fixture := filepath.Join(root, "fixture.yaml")
	if err := os.WriteFile(fixture, []byte(nodeFixture), 0600); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(view)
	view.console.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "recovery-settings-live.mjs"), ui.URL, fixture, evidence)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
	s, err := ReadStartupSettings(view.identity.Config)
	if err != nil || s.DatabasePath != database || s.VideoDir != video || s.nodeSnapshot != nodeFixture || !s.RestoreHostsOnStopOrExit || s.VideoBudgetBytes != 1073741824 || s.Theme != "dark" {
		t.Fatal("shared settings not persisted", err)
	}
	if attempts != 2 || *launches != 1 {
		t.Fatal("authorization retry", attempts, *launches)
	}
	if err := cacheproxy.CheckDatabaseSelection(database); err != nil {
		t.Fatal(err)
	}
	if view.console.store != nil || view.console.client != nil || view.console.terms.valid() {
		t.Fatal("settings initialized business")
	}
	if state := view.DesktopState(); state != (desktop.State{Unavailable: true, Theme: "dark"}) {
		t.Fatal("saved appearance was not published to the recovery tray", state)
	}
	if err := view.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hasManagedHosts(readHostsFixture(t, h.path)) {
		t.Fatal("exit policy not applied to private fixture")
	}
}
