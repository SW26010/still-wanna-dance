package desktop

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDynamicConsoleDiscovery(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"app":"stepstash-console","protocol":1}`)
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "session.json")
	owner := &Instance{lease: fakeLease{}, path: path}
	defer owner.Close()
	address := strings.TrimPrefix(s.URL, "http://")
	if err := owner.Publish(address); err != nil {
		t.Fatal(err)
	}
	for _, shouldOpen := range []bool{true, false} {
		opened := ""
		lease, err := acquireOrActivate(context.Background(), path, func() (Lease, error) { return nil, nil }, shouldOpen, func(url string) error { opened = url; return nil })
		if err != nil || lease != nil || (shouldOpen && opened != s.URL) || (!shouldOpen && opened != "") {
			t.Fatalf("activation: lease=%v error=%v opened=%q", lease, err, opened)
		}
	}
	// A new mutex owner must discard a crash leftover, even if its address is live.
	replacement, err := acquireOrActivate(context.Background(), path, func() (Lease, error) { return fakeLease{}, nil }, true, func(string) error { t.Fatal("opened stale address"); return nil })
	if err != nil || replacement == nil {
		t.Fatal("takeover failed", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale record retained", err)
	}
	if err := replacement.Publish(address); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal("close not idempotent", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("record retained after exit", err)
	}
}

func TestDiscoveryRejectsInvalidAndForeignRecords(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"app":"other","protocol":1}`) }))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "session.json")
	for _, data := range []string{
		`{`,
		`{"address":"example.com:80","pid":1}`,
		`{"address":"127.0.0.1:0","pid":1}`,
		fmt.Sprintf(`{"address":%q,"pid":0}`, strings.TrimPrefix(s.URL, "http://")),
		fmt.Sprintf(`{"address":%q,"pid":%d}`, strings.TrimPrefix(s.URL, "http://"), os.Getpid()+1),
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if got := readInstanceAddress(path); got != "" {
			t.Fatalf("accepted %s: %s", data, got)
		}
	}
	owner := &Instance{lease: fakeLease{}, path: path}
	defer owner.Close()
	if err := owner.Publish(strings.TrimPrefix(s.URL, "http://")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireOrActivate(ctx, path, func() (Lease, error) { return nil, nil }, true, func(string) error { t.Fatal("opened foreign service"); return nil }); err == nil {
		t.Fatal("foreign service accepted")
	}
}

func TestSystemAssignedConsolePort(t *testing.T) {
	first, err := net.Listen("tcp4", Address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.Listen("tcp4", Address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.Addr().String() == second.Addr().String() || !validAddress(first.Addr().String()) || !validAddress(second.Addr().String()) {
		t.Fatal("system did not allocate distinct loopback ports")
	}
}

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
