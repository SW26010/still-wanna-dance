package desktop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestProcessSessionProbeHelper(t *testing.T) {
	if os.Getenv("SWD_PROCESS_PROBE_CHILD") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestVRChatNativeProcessWait(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("swd-process-%d.exe", os.Getpid())
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		t.Fatal(err)
	}
	check := func(name string, session uint32, want bool) {
		t.Helper()
		pids, err := processIDsInSession(name, session)
		got := len(pids) != 0
		if err != nil || got != want {
			t.Fatalf("%s in session %d: running=%v, error=%v, want %v", name, session, got, err, want)
		}
	}
	start := func() (*exec.Cmd, io.WriteCloser) {
		t.Helper()
		cmd := exec.Command(path, "-test.run=^TestProcessSessionProbeHelper$")
		cmd.Env = append(os.Environ(), "SWD_PROCESS_PROBE_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		cmd.Stderr = os.Stderr
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = input.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
			t.Fatal("helper did not become ready", line, err)
		}
		return cmd, input
	}
	attach := func(ctx context.Context) <-chan error {
		t.Helper()
		process, err := findProcessInSession(name, session)
		if err != nil || process == nil {
			t.Fatal("failed to attach native wait", err)
		}
		done := make(chan error, 1)
		go func() {
			done <- errors.Join(process.Wait(ctx), process.Close())
		}()
		return done
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	check(name, session, false)
	first, input := start()
	firstWait := attach(ctx)
	second, _ := start()
	check(strings.ToUpper(name), session, true)
	check(name+".launcher", session, false)
	check(name, session+1, false)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := <-firstWait; err != nil {
		t.Fatal("normal exit did not signal the process wait", err)
	}
	check(name, session, true) // the other game instance still owns the session
	secondWait := attach(ctx)
	if err := second.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err == nil {
		t.Fatal("forced exit unexpectedly succeeded")
	}
	if err := <-secondWait; err != nil {
		t.Fatal("forced exit did not signal the process wait", err)
	}
	check(name, session, false)
	_, _ = start()
	stopping, stop := context.WithCancel(ctx)
	canceledWait := attach(stopping)
	stop()
	if err := <-canceledWait; !errors.Is(err, context.Canceled) {
		t.Fatal("native wait did not honor cancellation", err)
	}
	check(name, session, true) // stopping SWD does not terminate the game
}
