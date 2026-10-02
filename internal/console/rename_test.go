package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacySettingsAfterRename(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"default", `{}`, "stepstash-data"},
		{"configured", `{"storageDir":"custom-cache"}`, "custom-cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "stepstash-console.json")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := New(path, "127.0.0.1:18081")
			if err != nil {
				t.Fatal(err)
			}
			if c.settings.StorageDir != filepath.Join(root, tc.want) {
				t.Fatal(c.settings.StorageDir)
			}
		})
	}
}

func TestRestoreHostsAcrossRename(t *testing.T) {
	original := "127.0.0.1 localhost\r\n# user mapping\r\n127.0.0.1 example.org\r\n"
	mixed := original + "\r\n127.0.0.1 " + domains[0] + " " + legacyMarker + "\r\n"
	enabled, err := transformHosts(mixed, "enable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enabled, legacyMarker) || !strings.Contains(enabled, marker) {
		t.Fatal(enabled)
	}
	restored, err := transformHosts(enabled, "disable")
	if err != nil || restored != original {
		t.Fatalf("restore = %q, %v", restored, err)
	}
	for _, m := range []string{marker, legacyMarker} {
		if _, err := transformHosts("127.0.0.2 "+domains[0]+" "+m, "disable"); err == nil {
			t.Fatal("modified owned entry accepted")
		}
	}
}
