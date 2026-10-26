package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/workfiles"
)

func TestConfigCleanupBelongsToLiveLease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	p := apppaths.ForConfig(path)
	lease, err := LockConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	put := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	owned := []string{p.Config, p.Terms, p.State("throughput"), p.State("inventory"), p.State("batch-last-success"), p.State("batch-last-attempt")}
	for _, target := range owned {
		put(target)
		put(filepath.Join(filepath.Dir(target), workfiles.TempName(target)))
	}
	unknown := []string{filepath.Join(dir, workfiles.TempName("b.json")), filepath.Join(dir, ".settings-123"), p.ChannelKey, p.ChannelKey + ".corrupt-123", filepath.Join(p.StateDir, ".snapshot-123"), filepath.Join(p.StateDir, "throughput.json.corrupt-123")}
	for _, name := range []string{
		".batch.json.swd-tmp", ".attempt.json.swd-tmp",
		".setup-apply-" + strings.Repeat("a", 64) + ".json.swd-tmp",
		".setup-apply-" + strings.Repeat("a", 65) + ".json.swd-tmp",
		".setup-apply-" + strings.Repeat("A", 64) + ".json.swd-tmp",
		".setup-apply-" + strings.Repeat("g", 64) + ".json.swd-tmp",
		".setup-apply-" + strings.Repeat("a", 64) + ".json.swd-tmp.backup",
		".setup-apply.json.swd-tmp.backup",
	} {
		unknown = append(unknown, filepath.Join(p.StateDir, name))
	}
	other := apppaths.ForConfig(filepath.Join(dir, "other.json")).State("setup-apply")
	unknown = append(unknown, filepath.Join(filepath.Dir(other), workfiles.TempName(other)))
	for _, path := range unknown {
		put(path)
	}
	if err := workfiles.Write(p.State("batch-last-attempt"), []byte("new receipt")); err == nil {
		t.Fatal("abandoned slot did not reproduce write failure")
	}
	if second, err := LockConfig(path); err == nil {
		second.Close()
		t.Fatal("second owner acquired lock")
	}
	if err := lease.CleanWorkFiles(); err != nil {
		t.Fatal(err)
	}
	for _, target := range owned {
		if _, err := os.Stat(filepath.Join(filepath.Dir(target), workfiles.TempName(target))); !os.IsNotExist(err) {
			t.Fatal("work file remains", target, err)
		}
	}
	for _, target := range append(owned, unknown...) {
		if b, err := os.ReadFile(target); err != nil || string(b) != "preserve" {
			t.Fatal("protected file changed", target, err)
		}
	}
	for _, target := range owned[len(owned)-2:] {
		if err := workfiles.Write(target, []byte("new receipt")); err != nil {
			t.Fatal("receipt write still blocked after startup cleanup", target, err)
		}
	}
	lease.Close()
	tmp := filepath.Join(dir, workfiles.TempName(path))
	put(tmp)
	if err := lease.CleanWorkFiles(); err == nil {
		t.Fatal("cleanup accepted closed lease")
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatal(err)
	}
}

func TestConfigCleanupDoesNotTraverseWorkSlotDirectory(t *testing.T) {
	p := apppaths.ForConfig(filepath.Join(t.TempDir(), "config.json"))
	lease, err := LockConfig(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.CleanWorkFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(p.State("batch-last-attempt"))); !os.IsNotExist(err) {
		t.Fatal("cleanup created missing state directory", err)
	}
	target := p.State("batch-last-attempt")
	child := filepath.Join(filepath.Dir(target), workfiles.TempName(target), "keep")
	if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := lease.CleanWorkFiles(); err == nil {
		t.Fatal("unsafe receipt slot was accepted")
	}
	if data, err := os.ReadFile(child); err != nil || string(data) != "keep" {
		t.Fatal("unsafe directory was traversed or changed", err)
	}
}
