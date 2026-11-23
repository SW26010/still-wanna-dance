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

func TestReadLogSettingsDefaultsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if level, budget, err := ReadLogSettings(path); err != nil || level != applog.Info || budget != applog.DefaultTotalBytes {
		t.Fatal(level, budget, err)
	}
	if level, budget, err := ReadLogSettings(filepath.Dir(path)); err == nil || level != applog.Info || budget != 0 {
		t.Fatal("unreadable config returned a logging budget", level, budget, err)
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
		wantBudget := applog.DefaultTotalBytes
		if tc.fail {
			wantBudget = 0
		}
		if level, budget, err := ReadLogSettings(path); (err != nil) != tc.fail || level != tc.want || budget != wantBudget {
			t.Fatalf("%s: %v %d %v", tc.data, level, budget, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != tc.data {
			t.Fatal("bootstrap modified configuration", err)
		}
	}
}

func TestReadLogBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		value string
		want  int64 // zero indicates rejection before file logging
	}{
		{"null", applog.DefaultTotalBytes},
		{"1048576", applog.MaxBytes},
		{"67108864", 64 << 20},
		{"0", 0},
		{"-1", 0},
		{"1048575", 0},
		{"1048576.5", 0},
		{`"1048576"`, 0},
		{"9223372036854775808", 0},
	} {
		data := `{"schemaVersion":8,"logLevel":"off","logBudgetBytes":` + tc.value + `}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		level, budget, err := ReadLogSettings(path)
		if (err != nil) != (tc.want == 0) || level != applog.Off || budget != tc.want {
			t.Fatalf("logBudgetBytes=%s: level=%v budget=%d err=%v", tc.value, level, budget, err)
		}
		loaded, err := loadConfigFile(path)
		if err != nil || (tc.want != 0 && loaded.Settings.LogBudgetBytes != tc.want) || (tc.want == 0 && validateLogBudget(loaded.Settings.LogBudgetBytes) == nil) {
			t.Fatalf("full config load disagrees for %s: %v", tc.value, err)
		}
	}
}

func TestRecoveryRepairsLogBudget(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,"logBudgetBytes":0}`)
	w := recoveryRequest(view, "GET", "/api/recovery/settings", "")
	var state struct {
		Settings Settings
		Fields   map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || w.Code != 200 || state.Settings.LogBudgetBytes != 0 || state.Fields["logBudgetBytes"] == "" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	w = recoveryRequest(view, "POST", "/api/settings", `{"logBudgetBytes":1048576}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, budget, err := ReadLogSettings(view.identity.Config); budget != applog.MaxBytes || err != nil {
		t.Fatal(budget, err)
	}
}

func TestOptionalInvalidPreferencesSurviveUnrelatedSaves(t *testing.T) {
	c := testConsole(t)
	invalid := map[string]string{"theme": `"unknown"`, "logBudgetBytes": `"invalid"`, "vrcxImportDismissed": `12`}
	if err := updateConfigFile(c.configPath, func(fields map[string]json.RawMessage, _ bool) error {
		for key, value := range invalid {
			fields[key] = json.RawMessage(value)
		}
		fields["logLevel"] = json.RawMessage(`"off"`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if c.settings.Theme != "system" || c.vrcxImportDismissed || c.settings.LogBudgetBytes != 0 {
		t.Fatal("invalid preferences did not degrade independently")
	}
	if err := c.editor.saveSettingsPatch([]byte(`{"autoOpenWebUI":false}`)); err != nil {
		t.Fatal(err)
	}
	_, after, err := readConfigFile(c.configPath)
	if err != nil || string(after["autoOpenWebUI"]) != "false" {
		t.Fatal("unrelated setting was not saved", err)
	}
	for key, value := range invalid {
		if string(after[key]) != value {
			t.Fatal("unrelated save changed", key)
		}
	}
	for _, patch := range []string{`{"logLevel":"info"}`, `{"logBudgetBytes":0}`} {
		if err := c.editor.saveSettingsPatch([]byte(patch)); err == nil {
			t.Fatal("accepted invalid log budget", patch)
		}
	}
	if err := c.editor.saveSettingsPatch([]byte(`{"logLevel":"info","logBudgetBytes":1048576}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.editor.saveTheme("system"); err != nil {
		t.Fatal(err)
	}
	_, after, err = readConfigFile(c.configPath)
	if err != nil || string(after["theme"]) != `"system"` || string(after["logBudgetBytes"]) != `1048576` {
		t.Fatal("explicit repairs were not persisted", err)
	}
}

func TestLogSettingsSaveAndRestart(t *testing.T) {
	c := testConsole(t)
	if c.settings.LogLevel != "info" {
		t.Fatal("default", c.settings.LogLevel)
	}
	if err := updateConfigFile(c.configPath, func(fields map[string]json.RawMessage, _ bool) error {
		fields["logBudgetBytes"] = json.RawMessage(`67108864`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	post := func(value string) *httptest.ResponseRecorder {
		s := c.editor.savedSettings
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
		if c.settings.LogLevel != active || c.editor.savedSettings.LogLevel != value {
			t.Fatal("save changed the active level or lost the saved level")
		}
		level, budget, err := ReadLogSettings(c.configPath)
		if err != nil || level.String() != value || budget != 64<<20 {
			t.Fatal("bootstrap disagrees with saved settings", level, budget, err)
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
		if c.settings.LogLevel != value || c.settings != c.editor.savedSettings {
			t.Fatal("restart did not apply level")
		}
	}
	before, _ := os.ReadFile(c.configPath)
	if w := post("verbose"); w.Code != 400 {
		t.Fatal("accepted invalid level", w.Code)
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(before, after) || c.settings != c.editor.savedSettings {
		t.Fatal("rejected level changed configuration")
	}
}
