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
	c.Close()
	_, fields, err := readConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(fields["storageDir"]) != `"still-wanna-dance-data"` || string(fields["schemaVersion"]) != "1" {
		t.Fatal(fields)
	}
	if _, err := os.Stat(filepath.Join(root, "still-wanna-dance-data")); !os.IsNotExist(err) {
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
	for _, data := range []string{`{}`, `null`, `{"schemaVersion":0}`, `{"schemaVersion":999,"futurePreference":"keep"}`, `{"schemaVersion":"1"}`, `{"schemaVersion":1,`} {
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
	data := `{"schemaVersion":1,"futurePreference":{"enabled":true,"large":9007199254740993},"socks5Password":"old-secret"}`
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

func TestQueueStartupSettingsAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		data string
		want bool
	}{
		{`{"schemaVersion":1,"autoStartCDN":true}`, false},
		{`{"schemaVersion":1,"autoStartCDN":false,"autoStartQueue":true}`, true},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := New(path, "127.0.0.1:18081")
		if err != nil {
			t.Fatal(err)
		}
		if c.settings.AutoStartQueue != tc.want {
			t.Fatal(c.settings)
		}
		c.Close()
	}
}
