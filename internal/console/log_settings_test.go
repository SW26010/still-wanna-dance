package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/applog"
)

func TestReadLogLevelDefaultsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if level, err := ReadLogLevel(path); err != nil || level != applog.Info {
		t.Fatal(level, err)
	}
	for _, tc := range []struct {
		data string
		want applog.Level
		fail bool
	}{
		{`{"schemaVersion":8}`, applog.Info, false},
		{`{"schemaVersion":8,"logLevel":null}`, applog.Info, false},
		{`{"schemaVersion":8,"logLevel":""}`, applog.Info, false},
		{`{"schemaVersion":8,"logLevel":"debug"}`, applog.Debug, false},
		{`{"schemaVersion":8,"LOGLEVEL":"off"}`, applog.Off, false},
		{`{"schemaVersion":8,"logLevel":"warn"}`, applog.Warn, false},
		{`{"schemaVersion":8,"logLevel":"error"}`, applog.Error, false},
		{`{"schemaVersion":8,"logLevel":"verbose"}`, applog.Info, true},
		{`{"schemaVersion":8,"logLevel":false}`, applog.Info, true},
		{`{"schemaVersion":6,"logLevel":"off"}`, applog.Info, true},
		{`{"schemaVersion":7,"logLevel":"off"}`, applog.Info, true},
		{`{"schemaVersion":9,"logLevel":"off"}`, applog.Info, true},
		{`{`, applog.Info, true},
	} {
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		if level, err := ReadLogLevel(path); (err != nil) != tc.fail || level != tc.want {
			t.Fatalf("%s: %v %v", tc.data, level, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != tc.data {
			t.Fatal("bootstrap modified configuration", err)
		}
	}
}

func TestLogSettingsSaveAndRestart(t *testing.T) {
	c := testConsole(t)
	if c.settings.LogLevel != "info" {
		t.Fatal("default", c.settings.LogLevel)
	}
	post := func(value string) *httptest.ResponseRecorder {
		s := c.savedSettings
		s.LogLevel = value
		body, _ := json.Marshal(s)
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	for _, value := range []string{"off", "error", "warn", "debug", "info"} {
		active := c.settings.LogLevel
		if w := post(value); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if c.settings.LogLevel != active || c.savedSettings.LogLevel != value {
			t.Fatal("save changed the active level or lost the saved level")
		}
		level, err := ReadLogLevel(c.configPath)
		if err != nil || level.String() != value {
			t.Fatal("bootstrap disagrees with saved settings", level, err)
		}
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
		var state struct {
			RestartRequired bool
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.RestartRequired {
			t.Fatal("level change did not require restart", w.Body.String(), err)
		}
		c = restartTestConsole(t, c)
		if c.settings.LogLevel != value || c.settings != c.savedSettings {
			t.Fatal("restart did not apply level")
		}
	}
	before, _ := os.ReadFile(c.configPath)
	if w := post("verbose"); w.Code != 400 {
		t.Fatal("accepted invalid level", w.Code)
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(before, after) || c.settings != c.savedSettings {
		t.Fatal("rejected level changed configuration")
	}
}
