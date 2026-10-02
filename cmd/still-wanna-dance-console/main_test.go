package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompatibleConfigPath(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "still-wanna-dance-console.json")
	legacy := filepath.Join(root, "stepstash-console.json")
	check := func(want string) {
		t.Helper()
		got, err := compatibleConfigPath(current)
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	check(current)
	if err := os.WriteFile(legacy, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	check(legacy)
	if err := os.WriteFile(current, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	check(current)
}
