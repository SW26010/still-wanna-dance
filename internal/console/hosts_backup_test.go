package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostsBackupAndExactRestoration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	original := "127.0.0.1 localhost\r\n# unrelated StepStash note\r\n"
	old := original + "\r\n127.0.0.1 api.udon.dance " + marker + "\r\n"
	if err := os.WriteFile(p, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := p + ".stepstash-backup"
	if err := os.WriteFile(legacy, []byte("historical snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyHosts(p, "disable"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != original {
		t.Fatalf("restore: %q %v", b, err)
	}
	if err := applyHosts(p, "enable"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), "# SWD managed") {
		t.Fatalf("enable: %q %v", b, err)
	}
	if b, err := os.ReadFile(p + ".swd-backup"); err != nil || string(b) != old {
		t.Fatalf("first snapshot replaced: %q %v", b, err)
	}
	if b, err := os.ReadFile(legacy); err != nil || string(b) != "historical snapshot" {
		t.Fatalf("legacy snapshot changed: %q %v", b, err)
	}
}
