package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/workfiles"
)

func themeRequest(c *Console, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings/theme", strings.NewReader(body))
	r.Header.Set("X-SWD-Token", token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func TestThemeAutosavePersistsAndPreservesPendingSettings(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	if c.settings.Theme != "system" {
		t.Fatal("missing theme must follow the system")
	}
	for _, theme := range []string{"dark", "light", "system"} {
		w := themeRequest(c, `{"theme":"`+theme+`"}`, c.token)
		if w.Code != 200 || c.settings != c.savedSettings || c.DesktopState().Theme != theme {
			t.Fatal("theme must apply to active settings and tray without restart", w.Code, w.Body.String())
		}
	}
	next := c.savedSettings
	next.AutoOpenWebUI = !next.AutoOpenWebUI
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	active, saved := c.settings, c.savedSettings
	client := c.client
	if w := themeRequest(c, `{"theme":"dark"}`, c.token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	active.Theme, saved.Theme = "dark", "dark"
	if c.settings != active || c.savedSettings != saved || c.client != client || c.service != nil {
		t.Fatal("theme save changed runtime settings or applied a pending restart")
	}
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || string(fields["theme"]) != `"dark"` {
		t.Fatal("theme missing from configuration", err)
	}
	// An ordinary form omits the independently saved preference.
	next = c.savedSettings
	next.Theme = ""
	if err := c.save(next); err != nil || c.settings.Theme != "dark" {
		t.Fatal("ordinary save reset the theme", err)
	}
	reopened := restartTestConsole(t, c)
	if reopened.settings.Theme != "dark" || reopened.DesktopState().Theme != "dark" {
		t.Fatal("reopening lost the persisted theme")
	}
}

func TestThemeSaveFailureLeavesAppearanceAndFileUnchanged(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	old := c.settings
	for _, body := range []string{`{}`, `{"theme":null}`, `{"theme":""}`, `{"theme":"invalid"}`, `{"theme":"dark","autoAccelerate":false}`} {
		if w := themeRequest(c, body, c.token); w.Code != 400 {
			t.Fatal("invalid preference accepted", body, w.Code)
		}
	}
	if w := themeRequest(c, `{"theme":"dark"}`, "invalid-token"); w.Code != 403 {
		t.Fatal("theme save bypassed authorization", w.Code)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(c.configPath), workfiles.TempName(c.configPath)), 0700); err != nil {
		t.Fatal(err)
	}
	if w := themeRequest(c, `{"theme":"dark"}`, c.token); w.Code == 200 {
		t.Fatal("failed config write reported success")
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || string(before) != string(after) || c.settings != old || c.savedSettings != old || c.DesktopState().Theme != old.Theme {
		t.Fatal("failed save changed the theme or configuration", err)
	}
}

func TestThemeConfigDefaultAndValidation(t *testing.T) {
	for _, value := range []string{"missing", "null", `"light"`, `"dark"`, `"system"`, `"invalid"`} {
		t.Run(value, func(t *testing.T) {
			c := testConsole(t)
			path := c.configPath
			_, fields, err := readConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			delete(fields, "theme")
			if value != "missing" {
				fields["theme"] = json.RawMessage(value)
			}
			data, _ := json.Marshal(fields)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := newConsole(path, "127.0.0.1:18081", newFixtureDNS(), testWorkDirectory(t))
			if value == `"invalid"` {
				if err == nil {
					loaded.Close()
					t.Fatal("invalid theme accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			want := strings.Trim(value, `"`)
			if value == "missing" || value == "null" {
				want = "system"
			}
			if loaded.settings.Theme != want {
				t.Fatal("wrong loaded preference", loaded.settings.Theme)
			}
		})
	}
}
