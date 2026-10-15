package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

func TestPrepareAheadSettingsValidationAndPersistence(t *testing.T) {
	c := testConsole(t)
	for _, seconds := range []int{1, 30, 86400, 0} {
		next := c.settings
		next.PrepareAheadSeconds = seconds
		body, err := json.Marshal(next)
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
		want := seconds
		if want == 0 {
			want = 500
		}
		if c.settings.PrepareAheadSeconds != 500 || c.savedSettings.PrepareAheadSeconds != want {
			t.Fatal("save changed active lead or lost saved lead")
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		got := loaded.settings.PrepareAheadSeconds
		loaded.Close()
		if got != want {
			t.Fatalf("reloaded lead = %d, want %d", got, want)
		}
	}
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []json.RawMessage{[]byte(`-1`), []byte(`86401`), []byte(`1.5`), []byte(`"500"`)} {
		body, _ := json.Marshal(c.savedSettings)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		fields["prepareAheadSeconds"] = value
		body, _ = json.Marshal(fields)
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("accepted invalid lead %s: %d", value, w.Code)
		}
		after, err := os.ReadFile(c.configPath)
		if err != nil || !bytes.Equal(before, after) || c.savedSettings.PrepareAheadSeconds != 500 {
			t.Fatal("invalid lead changed saved settings", err)
		}
	}
}
