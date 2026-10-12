package console

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestSetupMissingStorageRejectsExternalJunctionAncestor(t *testing.T) {
	c := testConsole(t)
	root := t.TempDir()
	target, alias := filepath.Join(root, "external"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("junction: %s: %v", output, err)
	}
	path := filepath.Join(alias, "missing-parent", "media")
	before, _ := os.ReadFile(c.configPath)
	if _, err := cacheproxy.InspectStorage(path); err == nil {
		t.Fatal("inspection accepted junction ancestor")
	}
	check, err := c.inspectSetupStorage(path)
	if err != nil || check.Library.State != "failed" {
		t.Fatal("UI inspection did not reject ancestor", err)
	}
	p := setupTestPlan(c)
	p.StorageDir, p.CustomStorage = path, true
	if _, err := c.validateSetupPlan(p); err == nil {
		t.Fatal("navigation accepted ancestor")
	}
	if _, err := c.applySetupPlan(p); err == nil {
		t.Fatal("application accepted ancestor")
	}
	after, _ := os.ReadFile(c.configPath)
	entries, err := os.ReadDir(target)
	if !bytes.Equal(before, after) || err != nil || len(entries) != 0 {
		t.Fatal("rejection changed configuration or target", err)
	}
}

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
