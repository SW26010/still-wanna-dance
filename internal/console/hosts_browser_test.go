package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHostsRestorePermissionBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_HOSTS_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_HOSTS_BROWSER_EVIDENCE for real Console browser integration")
	}
	c := testConsole(t)
	owned, _ := transformHosts(hostsBaseline, "enable")
	h, launches := fixtureHostsController(t, owned)
	c.hosts = h
	c.settings.QueueWithGateway = false
	c.savedSettings = c.settings
	launch, attempts := h.launch, 0
	h.launch = func(ctx context.Context) (hostsConnection, error) {
		attempts++
		if attempts == 1 {
			return nil, errHostsAuthorizationCanceled
		}
		return launch(ctx)
	}
	c.AutoStart()
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "hosts-restore-live.mjs"), ui.URL, evidence)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	if attempts != 2 || *launches != 1 || !h.prepared.Load() || readHostsFixture(t, h.path) != owned {
		t.Fatal("repair changed hosts or startup/acceleration requested authorization", attempts, *launches)
	}
	select {
	case <-c.ShutdownRequested():
	default:
		t.Fatal("browser did not request exit")
	}
	if err := c.Close(); err != nil || readHostsFixture(t, h.path) != hostsBaseline || *launches != 1 {
		t.Fatal("exit did not reuse restored permission", err)
	}
}

func TestHostsPolicyBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_HOSTS_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_HOSTS_BROWSER_EVIDENCE for real Console browser integration")
	}
	c := testConsole(t)
	for _, phase := range []string{"default", "persistent"} {
		h, launches := fixtureHostsController(t, hostsBaseline)
		c.hosts = h
		c.videoAddress, c.httpsAddress, c.checksumURL = "127.0.0.1:0", "127.0.0.1:0", ""
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "hosts-live.mjs"), ui.URL, phase, evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		select {
		case <-c.ShutdownRequested():
		default:
			t.Fatal("browser did not request exit")
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if *launches != 1 || (readHostsFixture(t, h.path) == hostsBaseline) != (phase == "persistent") {
			t.Fatal("incorrect exit result", phase, *launches)
		}
		if phase == "default" {
			c, err = newConsoleWithWorkForTest(t, c.configPath, c.address)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if c.settings.RestoreHostsOnExit {
				t.Fatal("saved setting did not survive reopening")
			}
		}
	}
}

func TestVideoImportSuggestionBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_VIDEO_IMPORT_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_VIDEO_IMPORT_BROWSER_EVIDENCE for first-run import suggestion browser integration")
	}
	c := firstRunImportConsole(t)
	hosts := c.hosts
	before := readHostsFixture(t, hosts.path)
	for _, phase := range []string{"first-run", "reopened"} {
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "video-import-suggestion-live.mjs"), ui.URL, phase, evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		if videoImportSuggested(t, c) || c.service != nil || c.importStatus.Running || readHostsFixture(t, hosts.path) != before {
			t.Fatal("suggestion was not consumed or navigation started work/changed hosts")
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if phase == "first-run" {
			c, err = newConsole(c.configPath, c.address, newFixtureDNS(), testWorkDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			c.hosts = hosts
			c.videoAddress, c.httpsAddress, c.checksumURL = "127.0.0.1:0", "127.0.0.1:0", ""
		}
	}
}
