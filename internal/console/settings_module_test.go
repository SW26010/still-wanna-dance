package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLoadDoesNotPrepareResourcesOrRewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	loaded, err := loadConfigFile(path)
	if err != nil || loaded.Exists || loaded.Settings.PrepareAheadSeconds != 500 {
		t.Fatal("missing file did not return normalized defaults", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("loading defaults created files", err)
	}
	data := []byte(`{"schemaVersion":8,"databasePath":"db/library.sqlite","videoDir":"videos","vrchatLogDir":"logs","autoOpenWebUI":false,"downloadGrowIntervalMS":0,"localProtectionGraceSeconds":null,"vrcxImportDismissed":true,"futureValue":9007199254740993}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := loaded.Settings
	if !loaded.Exists || !loaded.VRCXImportDismissed || s.AutoOpenWebUI || s.DownloadGrowIntervalMS != 0 || s.LocalProtectionGraceSeconds != 300 || !s.QueueWithGateway {
		t.Fatal("load lost explicit values or defaults")
	}
	if s.DatabasePath != filepath.Join(root, "db", "library.sqlite") || s.VideoDir != filepath.Join(root, "videos") || s.VRChatLogDir != filepath.Join(root, "logs") {
		t.Fatal("paths did not resolve relative to the configuration")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatal("load prepared resources", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) {
		t.Fatal("load rewrote the file", err)
	}
}

func TestSettingsRequestOmissionSemantics(t *testing.T) {
	for _, variant := range []string{"missing", "null", "explicit"} {
		t.Run(variant, func(t *testing.T) {
			c := testConsole(t)
			if err := c.saveTheme("dark"); err != nil {
				t.Fatal(err)
			}
			active := c.settings
			fields := map[string]any{
				"requestRetentionMode": "unlimited", "videoStorageMode": "none", "aggressiveBlockReclamation": "auto",
				"databasePath": active.DatabasePath, "videoDir": active.VideoDir,
			}
			if variant != "missing" {
				for _, key := range []string{"downloadConcurrencySoftLimit", "downloadGrowIntervalMS", "localProtectionGraceSeconds", "restoreHostsOnStopOrExit", "retainFavorites", "theme", "autoOpenWebUI", "queueWithGateway"} {
					fields[key] = nil
				}
			}
			if variant == "explicit" {
				fields["downloadConcurrencySoftLimit"] = 32
				fields["downloadGrowIntervalMS"], fields["localProtectionGraceSeconds"] = 0, 0
				fields["restoreHostsOnStopOrExit"], fields["retainFavorites"] = false, false
				fields["autoOpenWebUI"], fields["queueWithGateway"] = true, true
			}
			body, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(body))
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			want := active
			want.AutoOpenWebUI, want.QueueWithGateway = false, false
			want.VRChatLogDir = "" // an omitted path selects automatic detection
			if variant == "explicit" {
				want.DownloadConcurrencySoftLimit = 32
				want.DownloadGrowIntervalMS, want.LocalProtectionGraceSeconds = 0, 0
				want.RestoreHostsOnStopOrExit, want.RetainFavorites = false, false
				want.AutoOpenWebUI, want.QueueWithGateway = true, true
			}
			active.RestoreHostsOnStopOrExit = want.RestoreHostsOnStopOrExit
			if c.savedSettings != want || c.settings != active {
				t.Fatal("request defaults or save/apply semantics changed")
			}
			before, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(`{}`))
			r.Header.Set("X-SWD-Token", c.token)
			w = httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal("incomplete form accepted", w.Code)
			}
			if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(after, before) || c.savedSettings != want {
				t.Fatal("incomplete request changed settings", err)
			}
		})
	}
}

func TestConcurrentConfigurationUpdatesPreserveIndependentChoices(t *testing.T) {
	c := testConsole(t)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	fields["futureValue"] = json.RawMessage(`9007199254740993`)
	// Unreadable protected data must also survive unrelated updates unchanged.
	fields["proxySecret"] = json.RawMessage(`{"version":999,"ciphertext":"unavailable"}`)
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	active, next := c.settings, c.savedSettings
	next.PrepareAheadSeconds++
	next.Theme = "" // ordinary forms retain the most recently saved theme
	start, results := make(chan struct{}), make(chan error, 3)
	for _, update := range []func() error{
		func() error { return c.saveSettings(next, true, false) },
		func() error { return c.saveTheme("dark") },
		c.dismissVRCXSuggestion,
	} {
		go func() { <-start; results <- update() }()
	}
	close(start)
	for range 3 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	next.Theme, active.Theme = "dark", "dark"
	if c.savedSettings != next || c.settings != active || !c.vrcxImportDismissed {
		t.Fatal("concurrent writes lost a choice or applied staged settings")
	}
	_, after, err := readConfigFile(c.configPath)
	if err != nil || string(after["futureValue"]) != "9007199254740993" {
		t.Fatal("unknown field lost precision", err)
	}
	var secret bytes.Buffer
	if err := json.Compact(&secret, after["proxySecret"]); err != nil || !bytes.Equal(secret.Bytes(), fields["proxySecret"]) {
		t.Fatal("unrelated updates replaced unreadable credentials", err)
	}
	c = restartTestConsole(t, c)
	if c.settings != next || !c.vrcxImportDismissed {
		t.Fatal("reopening lost concurrent updates")
	}
}

func TestVRCXDismissalRejectsMissingAndFutureConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfigVRCXDismissal(path); !os.IsNotExist(err) {
		t.Fatal("dismissal recreated a missing configuration", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing configuration was written", err)
	}
	data := []byte(`{"schemaVersion":999,"futureValue":9007199254740993}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigVRCXDismissal(path); err == nil {
		t.Fatal("dismissal accepted a future version")
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) {
		t.Fatal("dismissal changed a future configuration", err)
	}
}
