package console

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAutoUpstreamMigratesOnlyDiskMode(t *testing.T) {
	for _, backend := range []string{"external-socks5", "mihomo"} {
		for _, key := range []string{"upstreamMode", "UPSTREAMMODE"} {
			t.Run(backend+"/"+key, func(t *testing.T) {
				c := testConsole(t)
				if err := updateConfigFile(c.configPath, func(fields map[string]json.RawMessage, _ bool) error {
					fields[key] = json.RawMessage(`"auto"`)
					fields["proxyBackend"], _ = json.Marshal(backend)
					fields["socks5Address"] = json.RawMessage(`"127.0.0.1:1080"`)
					// Unreadable future envelopes must survive without implicit direct.
					fields["proxySecret"] = json.RawMessage(`{"version":999,"ciphertext":"keep-secret"}`)
					fields["proxyNodes"] = json.RawMessage(`{"version":999,"ciphertext":"keep-nodes"}`)
					fields["futureValue"] = json.RawMessage(`9007199254740993`)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				before, fields, err := readConfigFile(c.configPath)
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := loadConfigFile(c.configPath)
				if err != nil || !loaded.UpstreamAuto || loaded.Settings.UpstreamMode != "proxy" {
					t.Fatal("legacy disk mode not normalized", err)
				}
				if after, _ := os.ReadFile(c.configPath); !bytes.Equal(before, after) {
					t.Fatal("read-only load changed disk")
				}
				c = restartTestConsole(t, c)
				if c.settings.UpstreamMode != "proxy" || c.savedSettings != c.settings || !c.proxyModeMigrated {
					t.Fatal("startup did not apply migrated proxy mode")
				}
				if conn, err := c.upstreamDial(context.Background(), "tcp", "example.invalid:443"); err == nil {
					conn.Close()
					t.Fatal("unavailable migrated proxy allowed traffic")
				}
				if testDNS(c).queries.Load() != 0 {
					t.Fatal("migrated proxy queried direct DNS")
				}
				after, saved, err := readConfigFile(c.configPath)
				if err != nil {
					t.Fatal(err)
				}
				delete(fields, key)
				fields["upstreamMode"] = json.RawMessage(`"proxy"`)
				want, _ := json.Marshal(fields)
				got, _ := json.Marshal(saved)
				if !bytes.Equal(want, got) {
					t.Fatal("migration changed unrelated settings or encrypted envelopes")
				}
				w := httptest.NewRecorder()
				c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
				var status struct {
					ProxyModeMigrated bool
					RestartRequired   bool
				}
				if json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.ProxyModeMigrated || status.RestartRequired {
					t.Fatal("incorrect migration notice or restart state", w.Body.String())
				}
				browser := backend == "external-socks5" && key == "upstreamMode" && os.Getenv("SWD_PROXY_MODE_BROWSER_EVIDENCE") != ""
				if browser {
					checkProxyMigrationBrowser(t, c, "migrated")
				}
				c = restartTestConsole(t, c)
				if c.proxyModeMigrated || c.settings.UpstreamMode != "proxy" {
					t.Fatal("migration repeated or proxy mode lost on restart")
				}
				if again, _ := os.ReadFile(c.configPath); !bytes.Equal(after, again) {
					t.Fatal("restart rewrote migrated config")
				}
				if browser {
					checkProxyMigrationBrowser(t, c, "reopened")
				}
			})
		}
	}
}

func TestInvalidAutoConfigurationIsNotRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"schemaVersion":8,"upstreamMode":"auto","socks5Address":"invalid"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:18081"); err == nil {
		t.Fatal("invalid proxy accepted")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(data, after) {
		t.Fatal("invalid config changed")
	}
}

// Optional real Edge + Console check. Test consent and a temporary library are
// confined to this fixture; no gateway, monitor or production settings run.
func checkProxyMigrationBrowser(t *testing.T, c *Console, phase string) {
	t.Helper()
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "proxy-mode-live.mjs"), ui.URL, phase, os.Getenv("SWD_PROXY_MODE_BROWSER_EVIDENCE"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser %s: %v\n%s", phase, err, output)
	}
	t.Log(string(output))
}
