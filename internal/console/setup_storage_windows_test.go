package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSetupStorageJunctionCannotEnterPortable(t *testing.T) {
	root := t.TempDir()
	portable, alias := filepath.Join(root, "Portable"), filepath.Join(root, "external-alias")
	if err := os.Mkdir(portable, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, portable)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("junction: %s: %v", output, err)
	}
	if err := validateStorageLocation(filepath.Join(alias, "future", "library"), filepath.Join(portable, "data", "storage"), portable, true); err == nil {
		t.Fatal("junction bypassed custom storage boundary")
	}
	entries, err := os.ReadDir(portable)
	if err != nil || len(entries) != 0 {
		t.Fatalf("validation created media: %v %v", entries, err)
	}
}
