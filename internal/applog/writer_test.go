package applog

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	w, err := Open(path, 8, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"111\n", "222\n", "333\n", "444\n", "555\n", "666\n", "777\n"} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{"": "777\n", ".1": "555\n666\n", ".2": "333\n444\n"} {
		b, err := os.ReadFile(path + suffix)
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", suffix, b, err)
		}
	}
	w, err = Open(path, 8, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("888\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "777\n888\n" {
		t.Fatalf("append: %q %v", b, err)
	}
	files, err := filepath.Glob(path + "*")
	if err != nil || len(files) != 3 {
		t.Fatal(files, err)
	}
}

func TestConcurrentJSONRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := Open(path, 1024, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(w, nil))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				logger.Info("entry", "worker", id, "number", j)
			}
		}(i)
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 3; i++ {
		name := path
		if i > 0 {
			name = fmt.Sprintf("%s.%d", path, i)
		}
		b, err := os.ReadFile(name)
		if err != nil || len(b) == 0 || len(b) > 1024 {
			t.Fatalf("size=%d err=%v", len(b), err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if !json.Valid([]byte(line)) {
				t.Fatalf("torn record: %q", line)
			}
		}
	}
}

func TestRotationFailureReportedOnceAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	reports := 0
	w, err := Open(path, 4, 1, func(error) { reports++ })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("old\n")); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory prevents archive removal on every platform.
	if err := os.Mkdir(path+".1", 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(path+".1", "blocker")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := w.Write([]byte("new\n")); err == nil {
			t.Fatal("rotation should fail")
		}
	}
	if reports != 1 {
		t.Fatal(reports)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new\n" {
		t.Fatal(string(b), err)
	}
}
