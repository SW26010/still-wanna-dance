package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDownloadSettingsDefaultsAndZero(t *testing.T) {
	for _, tc := range []struct {
		name, fields    string
		limit, interval int
	}{
		{"missing", ``, 16, 250},
		{"null", `,"downloadSoftLimit":null,"downloadGrowIntervalMS":null`, 16, 250},
		{"zero_interval", `,"downloadSoftLimit":64,"downloadGrowIntervalMS":0`, 64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			if err := os.WriteFile(c.configPath, []byte(`{"schemaVersion":5`+tc.fields+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if c.settings.DownloadSoftLimit != tc.limit || c.settings.DownloadGrowIntervalMS != tc.interval {
				t.Fatal("incorrect loaded download settings", c.settings)
			}
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if c.settings.DownloadSoftLimit != tc.limit || c.settings.DownloadGrowIntervalMS != tc.interval {
				t.Fatal("save/restart lost download settings", c.settings)
			}
		})
	}
}

func TestDownloadSettingsValidationAndPersistence(t *testing.T) {
	c := testConsole(t)
	post := func(body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	for _, values := range [][2]int{{1, 0}, {64, 2000}, {64, 1}, {16, 250}} {
		next := c.settings
		next.DownloadSoftLimit, next.DownloadGrowIntervalMS = values[0], values[1]
		body, err := json.Marshal(next)
		if err != nil {
			t.Fatal(err)
		}
		if w := post(body); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if c.settings.DownloadSoftLimit != 16 || c.settings.DownloadGrowIntervalMS != 250 || c.savedSettings != next {
			t.Fatal("save changed runtime or lost download settings")
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		got := loaded.settings
		loaded.Close()
		if got != next {
			t.Fatal("reloaded settings differ", got, next)
		}
	}
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for key, invalid := range map[string][]string{
		"downloadSoftLimit":      {"-1", "0", "65", "1.5", `"16"`},
		"downloadGrowIntervalMS": {"-1", "2001", "0.5", `"250"`},
	} {
		for _, value := range invalid {
			body, _ := json.Marshal(c.savedSettings)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			fields[key] = json.RawMessage(value)
			body, _ = json.Marshal(fields)
			if w := post(body); w.Code != 400 {
				t.Fatalf("accepted %s=%s: %d", key, value, w.Code)
			}
			after, err := os.ReadFile(c.configPath)
			if err != nil || !bytes.Equal(before, after) || c.savedSettings != c.settings {
				t.Fatal("invalid input changed saved settings", err)
			}
		}
	}
}
