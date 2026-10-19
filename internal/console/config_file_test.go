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
	if !want.AutoStartCDN || !want.AutoStartQueue || want.PrepareAheadSeconds != 500 {
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
	if string(fields["prepareAheadSeconds"]) != "500" || string(fields["storageDir"]) != `"storage"` || string(fields["schemaVersion"]) != "5" || string(fields["autoStartCDN"]) != "true" || string(fields["autoStartQueue"]) != "true" {
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

func TestConfigurationRejectsUnsupportedWithoutOverwrite(t *testing.T) {
	for _, data := range []string{`{"schemaVersion":1}`, `{"schemaVersion":2}`, `{"schemaVersion":3}`, `{"schemaVersion":4}`, `{"schemaVersion":5,"PROXYSECRET":null}`, `{"schemaVersion":5,"SOCKS5Password":"secret"}`, `{}`, `null`, `{"schemaVersion":0}`, `{"schemaVersion":999,"futurePreference":"keep"}`, `{"schemaVersion":"1"}`, `{"schemaVersion":5,`} {
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
	data := `{"schemaVersion":5,"futurePreference":{"enabled":true,"large":9007199254740993},"prepareaheadseconds":999}`
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
		{"missing", `{"schemaVersion":5}`, true, true},
		{"null", `{"schemaVersion":5,"autoStartCDN":null,"autoStartQueue":null}`, true, true},
		{"missing queue with gateway on", `{"schemaVersion":5,"autoStartCDN":true}`, true, true},
		{"missing queue with gateway off", `{"schemaVersion":5,"autoStartCDN":false}`, false, true},
		{"missing gateway with queue off", `{"schemaVersion":5,"autoStartQueue":false}`, true, false},
		{"both off", `{"schemaVersion":5,"autoStartCDN":false,"autoStartQueue":false}`, false, false},
		{"gateway only", `{"schemaVersion":5,"autoStartCDN":true,"autoStartQueue":false}`, true, false},
		{"queue only", `{"schemaVersion":5,"autoStartCDN":false,"autoStartQueue":true}`, false, true},
		{"both on", `{"schemaVersion":5,"autoStartCDN":true,"autoStartQueue":true}`, true, true},
		{"obsolete queue field", `{"schemaVersion":5,"autoStartCDN":false,"queuePrefetchEnabled":false}`, false, true},
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
			if c.settings.AutoStartCDN != tc.wantCDN || c.settings.AutoStartQueue != tc.wantQueue {
				t.Fatalf("loaded startup settings: gateway=%v queue=%v", c.settings.AutoStartCDN, c.settings.AutoStartQueue)
			}
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if c.settings.AutoStartCDN != tc.wantCDN || c.settings.AutoStartQueue != tc.wantQueue {
				t.Fatal("save/restart changed startup settings")
			}
		})
	}
}
