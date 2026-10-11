package desktop

import (
	"os"
	"path/filepath"
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
	owned := []string{p.Config, p.Terms, p.State("throughput"), p.State("inventory"), p.State("batch"), p.State("attempt")}
	for _, target := range owned {
		put(target)
		put(filepath.Join(filepath.Dir(target), workfiles.TempName(target)))
	}
	unknown := []string{filepath.Join(dir, workfiles.TempName("b.json")), filepath.Join(dir, ".settings-123"), filepath.Join(p.Root, "channel.key"), filepath.Join(p.Root, "channel.key.corrupt-123"), filepath.Join(p.Root, "state", ".snapshot-123"), filepath.Join(p.Root, "state", "throughput.json.corrupt-123")}
	for _, path := range unknown {
		put(path)
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
