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
	c, err := New(path, "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	want := c.settings
	if !want.AutoAccelerate || !want.AutoOpenWebUI || !want.QueueWithGateway || want.PrepareAheadSeconds != 500 {
		t.Fatal("first launch should enable both startup preferences")
	}
	if want.AggressiveCacheReclamation != "auto" || want.VideoStorageMode != "none" || want.MaxCacheBytes != 0 {
		t.Fatal("first launch should not retain unprotected videos")
	}
	c.Close()
	_, fields, err := readConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(fields["prepareAheadSeconds"]) != "500" || string(fields["storageDir"]) != `"storage"` || string(fields["schemaVersion"]) != "6" || string(fields["autoAccelerate"]) != "true" || string(fields["queueWithGateway"]) != "true" {
		t.Fatal(fields)
	}
	if string(fields["aggressiveCacheReclamation"]) != `"auto"` || string(fields["videoStorageMode"]) != `"none"` || string(fields["maxCacheBytes"]) != "0" {
		t.Fatal("zero retention defaults were not persisted", fields)
	}
	if _, err := os.Stat(filepath.Join(root, "storage")); !os.IsNotExist(err) {
		t.Fatal("opening console created media store", err)
	}
	c, err = New(path, "127.0.0.1:18081")
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
	if err := c.saveSettings(next, true); err != nil {
		t.Fatal(err)
	}
	if !c.OpenWebUIOnLaunch() || c.savedSettings.AutoOpenWebUI {
		t.Fatal("save changed current launch or was not saved")
	}
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.OpenWebUIOnLaunch() {
		t.Fatal("new process ignored saved browser preference")
	}
}

func TestConfigurationRejectsUnsupportedWithoutOverwrite(t *testing.T) {
	for _, data := range []string{`{"schemaVersion":1}`, `{"schemaVersion":2}`, `{"schemaVersion":3}`, `{"schemaVersion":4}`, `{"schemaVersion":5}`, `{"schemaVersion":6,"PROXYSECRET":null}`, `{"schemaVersion":6,"SOCKS5Password":"secret"}`, `{}`, `null`, `{"schemaVersion":0}`, `{"schemaVersion":999,"futurePreference":"keep"}`, `{"schemaVersion":"1"}`, `{"schemaVersion":6,`} {
		t.Run(data, func(t *testing.T) {
			c := testConsole(t)
			if err := os.WriteFile(c.configPath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if other, err := New(c.configPath, c.address); err == nil {
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
	data := `{"schemaVersion":6,"futurePreference":{"enabled":true,"large":9007199254740993},"prepareaheadseconds":999}`
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
	reopened, err := New(c.configPath, c.address)
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
		{"missing", `{"schemaVersion":6}`, true, true},
		{"null", `{"schemaVersion":6,"autoAccelerate":null,"queueWithGateway":null}`, true, true},
		{"missing queue with gateway on", `{"schemaVersion":6,"autoAccelerate":true}`, true, true},
		{"missing queue with gateway off", `{"schemaVersion":6,"autoAccelerate":false}`, false, true},
		{"missing gateway with queue off", `{"schemaVersion":6,"queueWithGateway":false}`, true, false},
		{"both off", `{"schemaVersion":6,"autoAccelerate":false,"queueWithGateway":false}`, false, false},
		{"gateway only", `{"schemaVersion":6,"autoAccelerate":true,"queueWithGateway":false}`, true, false},
		{"queue only", `{"schemaVersion":6,"autoAccelerate":false,"queueWithGateway":true}`, false, true},
		{"both on", `{"schemaVersion":6,"autoAccelerate":true,"queueWithGateway":true}`, true, true},
		{"obsolete queue field", `{"schemaVersion":6,"autoAccelerate":false,"queuePrefetchEnabled":false}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := New(path, "127.0.0.1:18081")
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
