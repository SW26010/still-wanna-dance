package console

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredCacheReclamationIgnoredAndRemovedOnWrite(t *testing.T) {
	for _, value := range []string{`"on"`, `"off"`, `"auto"`, `"unknown"`, `true`} {
		for _, preferenceOnly := range []bool{false, true} {
			t.Run(value+"/"+map[bool]string{false: "settings", true: "preference"}[preferenceOnly], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.json")
				original := []byte(`{"schemaVersion":8,"aggressiveBlockReclamation":` + value + `,"AGGRESSIVEBLOCKRECLAMATION":"on","futurePreference":{"large":9007199254740993}}`)
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				loaded, err := loadConfigFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Settings.VideoStorageMode != "none" {
					t.Fatal("retired override changed defaults")
				}
				data, err := json.Marshal(loaded.Settings)
				if err != nil || bytes.Contains(bytes.ToLower(data), []byte("aggressiveblockreclamation")) {
					t.Fatal("retired setting exposed", err)
				}
				before, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, original) {
					t.Fatal("load rewrote old configuration", err)
				}
				if preferenceOnly {
					err = saveConfigVRCXDismissal(path)
				} else {
					err = saveConfigSettings(path, loaded.Settings)
				}
				if err != nil {
					t.Fatal(err)
				}
				data, fields, err := readConfigFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(bytes.ToLower(data), []byte("aggressiveblockreclamation")) {
					t.Fatal("retired override survived save")
				}
				var future struct {
					Large json.Number `json:"large"`
				}
				if err := json.Unmarshal(fields["futurePreference"], &future); err != nil || future.Large.String() != "9007199254740993" {
					t.Fatal("unknown field changed", err)
				}
				if _, err := loadConfigFile(path); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
