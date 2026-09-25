package desktop

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestMutexProcess(t *testing.T) {
	name := os.Getenv("STEPSTASH_MUTEX_TEST_CHILD")
	if name == "" {
		return
	}
	lease, err := acquire(name)
	if err != nil || lease == nil {
		t.Fatal("child lock", err)
	}
	defer lease.Close()
	fmt.Println("READY")
	var b [1]byte
	os.Stdin.Read(b[:])
}

func TestWindowsMutexOwnershipAndCrashRecovery(t *testing.T) {
	name := fmt.Sprintf(`Local\StepStash.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
	first, err := acquire(name)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if other, err := acquire(name); other != nil || err != nil {
		t.Fatal("same-process duplicate acquired", err)
	}
	released := make(chan error, 1)
	go func() { released <- first.Close() }()
	if err = <-released; err != nil {
		t.Fatal("cross-thread close", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMutexProcess$")
	cmd.Env = append(os.Environ(), "STEPSTASH_MUTEX_TEST_CHILD="+name)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child start timeout")
	}
	if other, err := acquire(name); other != nil || err != nil {
		t.Fatal("cross-process duplicate acquired", err)
	}
	// No graceful release: the OS must make the abandoned owner recoverable.
	cmd.Process.Kill()
	cmd.Wait()
	last, err := acquire(name)
	if err != nil || last == nil {
		t.Fatal("crash prevented restart", err)
	}
	last.Close()
}

func TestPortOwnerIdentifiesCurrentProcess(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	owner := PortOwner(l.Addr().String())
	if owner == nil || owner.PID != uint32(os.Getpid()) || owner.Name == "" {
		t.Fatalf("wrong owner: %+v", owner)
	}
}

func TestLiveTrayLifecycle(t *testing.T) {
	if os.Getenv("STEPSTASH_TRAY_CHECK") != "1" {
		t.Skip("opt-in interactive Windows shell test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var closes atomic.Int32
	err := Run(ctx, Options{URL: "http://127.0.0.1:" + strconv.Itoa(18081), State: func() State { return State{} }, Command: func(int) error { return nil }, Shutdown: func() { closes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("cleanup ran %d times", closes.Load())
	}
}

func TestCanceledLogoffKeepsTrayAlive(t *testing.T) {
	tt := &tray{}
	if tt.dispatch(0, 0x11, 0, 0) != 1 {
		t.Fatal("logoff query rejected")
	}
	tt.dispatch(0, 0x16, 0, 0)
	if tt.closing {
		t.Fatal("canceled logoff stopped the app")
	}
}
