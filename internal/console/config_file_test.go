package console

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstLaunchPersistsNormalizedConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	c, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	want := c.settings
	if want.AutoAccelerate || !want.AutoOpenWebUI || !want.QueueWithGateway || want.PrepareAheadSeconds != 500 {
		t.Fatal("first launch should disable automatic acceleration and enable WebUI opening and queue following")
	}
	if want.AggressiveBlockReclamation != "auto" || want.VideoStorageMode != "none" || want.VideoBudgetBytes != 0 {
		t.Fatal("first launch should not retain unprotected videos")
	}
	c.Close()
	_, fields, err := readConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored Settings
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &stored) != nil {
		t.Fatal("could not read initial settings", err)
	}
	if string(fields["prepareAheadSeconds"]) != "500" || stored.DatabasePath != filepath.Join("data", "database", "storage.sqlite") || stored.VideoDir != "videos" || fields["storageDir"] != nil || string(fields["schemaVersion"]) != "8" || string(fields["autoAccelerate"]) != "false" || string(fields["queueWithGateway"]) != "true" {
		t.Fatal(fields)
	}
	if string(fields["aggressiveBlockReclamation"]) != `"auto"` || string(fields["videoStorageMode"]) != `"none"` || string(fields["videoBudgetBytes"]) != "0" {
		t.Fatal("zero retention defaults were not persisted", fields)
	}
	if string(fields["vrchatLogDir"]) != `""` || string(fields["downloadConcurrencySoftLimit"]) != "16" || string(fields["restoreHostsOnStopOrExit"]) != "true" {
		t.Fatal("new field defaults were not persisted", fields)
	}
	for _, name := range []string{"logDir", "manualLogDir", "maxCacheBytes", "aggressiveCacheReclamation", "downloadSoftLimit", "restoreHostsOnExit"} {
		if fields[name] != nil {
			t.Fatalf("obsolete field %s was written", name)
		}
	}
	if _, err := os.Stat(want.DatabasePath); err != nil {
		t.Fatal("first launch did not prepare the default database", err)
	}
	c, err = newConsoleWithWorkForTest(t, path, "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.settings != want {
		t.Fatal("default settings changed on restart")
	}
}

func TestSavedAutoOpenAppliesOnNextLaunch(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	if !c.OpenWebUIOnLaunch() {
		t.Fatal("automatic browser opening should default on")
	}
	next := c.savedSettings
	next.AutoOpenWebUI = false
	if err := c.saveSettings(next, true, false); err != nil {
		t.Fatal(err)
	}
	if !c.OpenWebUIOnLaunch() || c.savedSettings.AutoOpenWebUI {
		t.Fatal("save changed current launch or was not saved")
	}
	reopened, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.OpenWebUIOnLaunch() {
		t.Fatal("new process ignored saved browser preference")
	}
	if err := os.Remove(reopened.files().Terms); err != nil {
		t.Fatal(err)
	}
	unaccepted, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer unaccepted.Close()
	if !unaccepted.OpenWebUIOnLaunch() || unaccepted.settings.AutoOpenWebUI {
		t.Fatal("missing consent should open confirmation without changing the preference")
	}
	if err := unaccepted.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	if unaccepted.OpenWebUIOnLaunch() {
		t.Fatal("valid consent should restore the saved browser preference")
	}
}

func TestConfigurationRejectsUnsupportedWithoutOverwrite(t *testing.T) {
	for _, data := range []string{`{"schemaVersion":6}`, `{"schemaVersion":7}`, `{"schemaVersion":8,"storageDir":"storage"}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readConfigFile(path); err == nil {
			t.Fatal("obsolete configuration accepted")
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != data {
			t.Fatal("obsolete configuration changed", err)
		}
	}
	for _, data := range []string{`{"schemaVersion":1}`, `{"schemaVersion":2}`, `{"schemaVersion":3}`, `{"schemaVersion":4}`, `{"schemaVersion":5}`, `{"schemaVersion":8,"PROXYSECRET":null}`, `{"schemaVersion":8,"SOCKS5Password":"secret"}`, `{}`, `null`, `{"schemaVersion":0}`, `{"schemaVersion":999,"futurePreference":"keep"}`, `{"schemaVersion":"1"}`, `{"schemaVersion":8,`} {
		t.Run(data, func(t *testing.T) {
			c := testConsole(t)
			if err := os.WriteFile(c.configPath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if other, err := newConsoleWithWorkForTest(t, c.configPath, c.address); err == nil {
				other.Close()
				t.Fatal("accepted unsupported config")
			}
			if err := c.save(c.settings); err == nil {
				t.Fatal("overwrote unsupported config")
			}
			after, err := os.ReadFile(c.configPath)
			if err != nil || !bytes.Equal(after, []byte(data)) {
				t.Fatal("configuration changed", err)
			}
		})
	}
}

func TestConfigurationPreservesUnknownFieldsAndClearsPassword(t *testing.T) {
	c := testConsole(t)
	data := `{"schemaVersion":8,"futurePreference":{"enabled":true,"large":9007199254740993},"prepareaheadseconds":999}`
	if err := os.WriteFile(c.configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s := c.settings
	s.SOCKS5Password = ""
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	_, fields, err := readConfigFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, fields["futurePreference"]); err != nil {
		t.Fatal(err)
	}
	if compact.String() != `{"enabled":true,"large":9007199254740993}` {
		t.Fatal(compact.String())
	}
	if _, ok := fields["socks5Password"]; ok {
		t.Fatal("cleared password survived")
	}
	if _, ok := fields["SOCKS5Password"]; ok {
		t.Fatal("case alias retained a cleared password")
	}
	reopened, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.settings.SOCKS5Password != "" || reopened.settings.PrepareAheadSeconds != s.PrepareAheadSeconds {
		t.Fatal("case alias overrode saved settings", reopened.settings.PrepareAheadSeconds)
	}
	entries, err := os.ReadDir(filepath.Dir(c.configPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".settings-") {
			t.Fatal("temporary config survived save")
		}
	}
}

func TestStartupSettingsDefaultAndRemainIndependent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		data      string
		wantCDN   bool
		wantQueue bool
	}{
		{"missing", `{"schemaVersion":8}`, false, true},
		{"null", `{"schemaVersion":8,"autoAccelerate":null,"queueWithGateway":null}`, false, true},
		{"missing queue with gateway on", `{"schemaVersion":8,"autoAccelerate":true}`, true, true},
		{"missing queue with gateway off", `{"schemaVersion":8,"autoAccelerate":false}`, false, true},
		{"missing gateway with queue off", `{"schemaVersion":8,"queueWithGateway":false}`, false, false},
		{"both off", `{"schemaVersion":8,"autoAccelerate":false,"queueWithGateway":false}`, false, false},
		{"gateway only", `{"schemaVersion":8,"autoAccelerate":true,"queueWithGateway":false}`, true, false},
		{"queue only", `{"schemaVersion":8,"autoAccelerate":false,"queueWithGateway":true}`, false, true},
		{"both on", `{"schemaVersion":8,"autoAccelerate":true,"queueWithGateway":true}`, true, true},
		{"obsolete queue field", `{"schemaVersion":8,"autoAccelerate":false,"queuePrefetchEnabled":false}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:18081")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if c.settings.AutoAccelerate != tc.wantCDN || c.settings.QueueWithGateway != tc.wantQueue {
				t.Fatalf("loaded startup settings: gateway=%v queue=%v", c.settings.AutoAccelerate, c.settings.QueueWithGateway)
			}
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if c.settings.AutoAccelerate != tc.wantCDN || c.settings.QueueWithGateway != tc.wantQueue {
				t.Fatal("save/restart changed startup settings")
			}
		})
	}
}
