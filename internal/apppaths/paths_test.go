package apppaths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutIsLazyAndSeparatesSiblingConfigurations(t *testing.T) {
	root := t.TempDir()
	p := ForConfig(DefaultConfig(filepath.Join(root, "SWD.exe")))
	if p.Config != filepath.Join(root, "data", "config.json") || p.Root != filepath.Join(root, "data") || p.DefaultStorage != "storage" || p.Log != filepath.Join(root, "data", "logs", "app.log") {
		t.Fatal(p)
	}
	for _, name := range []string{"a.json", "b.json", "a.yaml"} {
		q := ForConfig(filepath.Join(p.Root, name))
		if q.Root != filepath.Join(p.Root, name+".d") || q.DefaultStorage != filepath.Join(name+".d", "storage") {
			t.Fatal(q)
		}
		if q.Terms == p.Terms || q.Log == p.Log || q.Lock == p.Lock || q.State("batch") == p.State("batch") {
			t.Fatal("shared companions", q)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("path resolution created files", entries, err)
	}
}

func TestLegacyInstallIsDiagnosedWithoutChanges(t *testing.T) {
	for _, name := range []string{"still-wanna-dance-console.json", "stepstash-console.json", "still-wanna-dance-data", "stepstash-data"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := CheckLegacyInstall(root); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := CheckLegacyInstall(root); err == nil {
				t.Fatal("legacy marker accepted")
			}
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || string(data) != "preserve" {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
		})
	}
}

func TestReservedCompanionsCannotBeConfigurations(t *testing.T) {
	for _, name := range []string{"terms.json", "CHANNEL.KEY", "config.lock", "storage", "logs", "state"} {
		if err := ValidateConfigPath(filepath.Join(t.TempDir(), name)); err == nil {
			t.Fatal(name)
		}
	}
}
