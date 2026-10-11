package desktop

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/workfiles"
)

func TestConfigWorkFilesAfterProcessTermination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	child := exec.Command(os.Args[0], "-test.run=^TestConfigWorkFilesCrashChild$")
	child.Env = append(os.Environ(), "SWD_TEST_CRASH_CONFIG="+path)
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
	if other, err := LockConfig(path); err == nil {
		other.Close()
		t.Fatal("child did not hold lease")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	lease, err := LockConfig(path)
	if err != nil {
		t.Fatal("crash did not release lease", err)
	}
	defer lease.Close()
	if err := lease.CleanWorkFiles(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "last committed settings" {
		t.Fatal("lost committed configuration", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), workfiles.TempName(path))); !os.IsNotExist(err) {
		t.Fatal("work file survived recovery", err)
	}
	if err := workfiles.Write(path, []byte("next committed settings")); err != nil {
		t.Fatal("cannot save after recovery", err)
	}
}

func TestConfigWorkFilesCrashChild(t *testing.T) {
	path := os.Getenv("SWD_TEST_CRASH_CONFIG")
	if path == "" {
		return
	}
	lease, err := LockConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := workfiles.Write(path, []byte("last committed settings")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), workfiles.TempName(path)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("interrupted settings")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
}
