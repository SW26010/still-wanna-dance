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
		case <-c.ExitRequested():
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
