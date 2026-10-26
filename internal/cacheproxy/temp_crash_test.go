package cacheproxy

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTemporaryMediaAfterProcessTermination(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()
	store, err := CreateStore(storagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	videoDir := filepath.Join(root, "videos")
	if err := os.Mkdir(videoDir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	writeTestFile(t, source, "protected source")
	child := exec.Command(os.Args[0], "-test.run=^TestTemporaryMediaCrashChild$")
	child.Env = append(os.Environ(), "SWD_TEST_CRASH_STORAGE="+root, "SWD_TEST_CRASH_WORK="+workDir, "SWD_TEST_CRASH_SOURCE="+source)
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("child failed: %q", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child startup timed out")
	}
	if other, err := OpenStore(storagePath(root)); err == nil {
		other.Close()
		t.Fatal("child did not hold database lock")
	}
	if other, err := OpenWorkDirectory(workDir, nil); err == nil {
		other.Close()
		t.Fatal("child did not hold work lock")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	cfg := fixtureConfig()
	cfg.Store, err = OpenStore(storagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Store.Close()
	cfg.Work, err = OpenWorkDirectory(workDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Work.Close()
	cfg.VideoDir = videoDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal("recovery failed", err)
	}
	defer s.Close()
	entries, err := os.ReadDir(cfg.tempDir())
	if err != nil || len(entries) != 1 || entries[0].Name() != ".lock" {
		t.Fatal("abandoned blocks remain", entries, err)
	}
	entries, err = os.ReadDir(cfg.publishDir())
	if err != nil || len(entries) != 0 {
		t.Fatal("abandoned publication files remain", entries, err)
	}
	if b, err := os.ReadFile(source); err != nil || string(b) != "protected source" {
		t.Fatal("source changed", err)
	}
	var integrity string
	if err := s.usage.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("database damaged", integrity, err)
	}
}

func TestTemporaryMediaCrashChild(t *testing.T) {
	root := os.Getenv("SWD_TEST_CRASH_STORAGE")
	if root == "" {
		return
	}
	cfg := fixtureConfig()
	var err error
	cfg.Store, err = OpenStore(storagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Store.Close()
	cfg.VideoDir = filepath.Join(root, "videos")
	cfg.Work, err = OpenWorkDirectory(os.Getenv("SWD_TEST_CRASH_WORK"), cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Work.Close()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, item := range []struct{ dir, pattern string }{{cfg.tempDir(), "download-block-*.part"}, {cfg.publishDir(), "download-publish-*.part"}} {
		f, err := os.CreateTemp(item.dir, item.pattern)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write([]byte("partial video")); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	for _, linked := range []bool{false, true} {
		dir, err := os.MkdirTemp(cfg.publishDir(), "import-")
		if err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(dir, "video")
		if linked {
			err = os.Link(os.Getenv("SWD_TEST_CRASH_SOURCE"), stage)
		} else {
			err = os.WriteFile(stage, []byte("partial copy"), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
}
