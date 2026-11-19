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
	if want.AutoAccelerate || !want.AutoOpenWebUI || want.PrepareAheadSeconds != 500 {
		t.Fatal("first launch should disable automatic acceleration and enable WebUI opening and queue following")
	}
	if want.VideoStorageMode != "none" || want.VideoBudgetBytes != 0 {
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
	if string(fields["prepareAheadSeconds"]) != "500" || stored.DatabasePath != filepath.Join("data", "database", "storage.sqlite") || stored.VideoDir != "videos" || fields["storageDir"] != nil || string(fields["schemaVersion"]) != "8" || string(fields["autoAccelerate"]) != "false" || fields["queueWithGateway"] != nil {
		t.Fatal(fields)
	}
	if fields["aggressiveBlockReclamation"] != nil || string(fields["videoStorageMode"]) != `"none"` || string(fields["videoBudgetBytes"]) != "0" {
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
	next := c.editor.savedSettings
	next.AutoOpenWebUI = false
	if err := c.editor.saveSettings(next, true, nil); err != nil {
		t.Fatal(err)
	}
	if !c.OpenWebUIOnLaunch() || c.editor.savedSettings.AutoOpenWebUI {
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
			if err := c.editor.save(c.settings); err == nil {
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
	if err := c.editor.save(s); err != nil {
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

func TestStartupSettingsIgnoreRetiredQueueSetting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		wantCDN bool
	}{
		{"missing", `"futurePreference":9007199254740993`, false},
		{"null", `"autoAccelerate":null,"queueWithGateway":null`, false},
		{"off", `"autoAccelerate":true,"queueWithGateway":false`, true},
		{"on", `"autoAccelerate":false,"queueWithGateway":true`, false},
		{"invalid legacy value", `"autoAccelerate":true,"QueueWithGateway":{"invalid":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data := `{"schemaVersion":8,` + tc.data + `}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadConfigFile(path)
			if err != nil || loaded.Settings.AutoAccelerate != tc.wantCDN {
				t.Fatal("loaded startup setting", loaded.Settings.AutoAccelerate, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != data {
				t.Fatal("loading changed configuration", err)
			}
			if err := saveConfigSettings(path, loaded.Settings); err != nil {
				t.Fatal(err)
			}
			_, fields, err := readConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for key := range fields {
				if strings.EqualFold(key, "queueWithGateway") {
					t.Fatal("save retained retired key", key)
				}
			}
			if tc.name == "missing" && string(fields["futurePreference"]) != "9007199254740993" {
				t.Fatal("save changed unrelated preference")
			}
			loaded, err = loadConfigFile(path)
			if err != nil || loaded.Settings.AutoAccelerate != tc.wantCDN {
				t.Fatal("save changed startup setting", loaded.Settings.AutoAccelerate, err)
			}
		})
	}
}
