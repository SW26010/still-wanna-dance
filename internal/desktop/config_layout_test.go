package desktop

import (
	"path/filepath"
	"testing"
)

func TestSiblingConfigurationsHaveIndependentLocks(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"config.json", "a.json", "b.json"} {
		path := filepath.Join(root, name)
		lease, err := LockConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { lease.Close() })
		if duplicate, err := LockConfig(path); err == nil {
			duplicate.Close()
			t.Fatal("same configuration was not locked", name)
		}
	}
}
