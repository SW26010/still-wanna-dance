package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestDynamicConsoleDiscovery(t *testing.T) {
	identity, err := NewIdentity(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(identity)
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "session.json")
	owner := &Instance{lease: fakeLease{}, path: path, identity: identity}
	defer owner.Close()
	address := strings.TrimPrefix(s.URL, "http://")
	if err := owner.Publish(address); err != nil {
		t.Fatal(err)
	}
	for _, shouldOpen := range []bool{true, false} {
		opened := ""
		lease, err := acquireOrActivate(context.Background(), path, identity, func() (Lease, error) { return nil, nil }, shouldOpen, func(url string) error { opened = url; return nil })
		if err != nil || lease != nil || (shouldOpen && opened != s.URL) || (!shouldOpen && opened != "") {
			t.Fatalf("activation: lease=%v error=%v opened=%q", lease, err, opened)
		}
	}
	// A new mutex owner must discard a crash leftover, even if its address is live.
	replacement, err := acquireOrActivate(context.Background(), path, identity, func() (Lease, error) { return fakeLease{}, nil }, true, func(string) error { t.Fatal("opened stale address"); return nil })
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
	identity, err := NewIdentity(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
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
		if got := readInstanceRecord(path); got != nil {
			t.Fatalf("accepted %s: %+v", data, got)
		}
	}
	owner := &Instance{lease: fakeLease{}, path: path, identity: identity}
	defer owner.Close()
	if err := owner.Publish(strings.TrimPrefix(s.URL, "http://")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireOrActivate(ctx, path, identity, func() (Lease, error) { return nil, nil }, true, func(string) error { t.Fatal("opened foreign service"); return nil }); err == nil {
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
	name := os.Getenv("SWD_MUTEX_TEST_CHILD")
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
	name := fmt.Sprintf(`Local\Still Wanna Dance.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
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
	cmd.Env = append(os.Environ(), "SWD_MUTEX_TEST_CHILD="+name)
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
	if os.Getenv("SWD_TRAY_CHECK") != "1" {
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

func TestTrayAccelerationTooltip(t *testing.T) {
	for _, tc := range []struct {
		state State
		busy  bool
		want  string
	}{
		{State{}, false, "加速未运行"},
		{State{CDN: true}, false, "加速未就绪"},
		{State{CDN: true, Ready: true}, false, "加速运行中"},
		{State{Activating: true}, false, "正在处理…"},
		{State{}, true, "正在处理…"},
		{State{CDN: true, Ready: true, Batch: true}, false, "加速运行中 · 批量任务运行中"},
	} {
		tt := &tray{options: Options{State: func() State { return tc.state }}}
		tt.busy.Store(tc.busy)
		if got := tt.tooltip(); got != "Still Wanna Dance · "+tc.want {
			t.Fatal(got)
		}
		tt.closing = true
		if got := tt.tooltip(); got != "Still Wanna Dance · 正在退出" {
			t.Fatal(got)
		}
	}
}

func TestLiveTrayAccelerationCommand(t *testing.T) {
	if os.Getenv("SWD_TRAY_CHECK") != "1" {
		t.Skip("opt-in interactive Windows shell test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release, finished := make(chan int, 1), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		defer cancel()
		class := wide(fmt.Sprintf("SWD.Tray.%d", os.Getpid()))
		var hwnd uintptr
		for hwnd == 0 && ctx.Err() == nil {
			hwnd, _, _ = user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(class)), 0)
			if hwnd == 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if hwnd == 0 {
			done <- fmt.Errorf("tray window unavailable: %w", ctx.Err())
			return
		}
		user32.NewProc("SendMessageW").Call(hwnd, wmCommand, StartAcceleration, 0)
		select {
		case id := <-entered:
			// A second native command while the first is pending must be ignored.
			user32.NewProc("SendMessageW").Call(hwnd, wmCommand, StartAcceleration, 0)
			close(release)
			<-finished
			if id != StartAcceleration || calls.Load() != 1 {
				done <- fmt.Errorf("command=%d calls=%d", id, calls.Load())
				return
			}
			done <- nil
		case <-ctx.Done():
			close(release)
			done <- fmt.Errorf("tray command not dispatched: %w", ctx.Err())
		}
	}()
	err := Run(ctx, Options{
		State: func() State { return State{TermsAccepted: true} },
		Command: func(id int) error {
			calls.Add(1)
			entered <- id
			<-release
			close(finished)
			return nil
		},
		Shutdown: func() {},
	})
	if err != nil {
		cancel()
	}
	if commandErr := <-done; err != nil || commandErr != nil {
		t.Fatal(err, commandErr)
	}
}

func TestLiveTrayFailureReleasesBusyBeforeDialogDismissal(t *testing.T) {
	if os.Getenv("SWD_TRAY_CHECK") != "1" {
		t.Skip("opt-in interactive Windows shell test")
	}
	tt := &tray{options: Options{
		State: func() State { return State{TermsAccepted: true} },
		Command: func(int) error {
			return errors.New("托盘失败状态回归测试：无需操作，此窗口会自动关闭")
		},
	}}
	// Find only this test process's dialog; never dismiss another SWD instance.
	var dialog uintptr
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == uint32(os.Getpid()) {
			var class [256]uint16
			user32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
			if syscall.UTF16ToString(class[:]) == "#32770" {
				dialog = hwnd
				return 0
			}
		}
		return 1
	})
	tt.command(StartAcceleration)
	deadline := time.Now().Add(5 * time.Second)
	for dialog == 0 && time.Now().Before(deadline) {
		user32.NewProc("EnumWindows").Call(callback, 0)
		if dialog == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if dialog == 0 {
		t.Fatal("failed command did not display its native error dialog")
	}
	defer user32.NewProc("SendMessageW").Call(dialog, wmClose, 0, 0)
	item := Menu(tt.options.State(), tt.busy.Load())[2]
	if item.Disabled || item.Label != "开始加速" || tt.tooltip() != "Still Wanna Dance · 加速未运行" {
		t.Fatalf("completed failure still shown as processing while error dialog is open: %+v", item)
	}
}

func TestTrayAccelerationRequiresConsent(t *testing.T) {
	var accepted atomic.Bool
	called := make(chan int, 1)
	tt := &tray{options: Options{
		State: func() State { return State{TermsAccepted: accepted.Load()} },
		Command: func(id int) error {
			called <- id
			return nil
		},
	}}
	tt.dispatch(0, wmCommand, StartAcceleration, 0)
	if tt.busy.Load() {
		t.Fatal("unaccepted command became busy")
	}
	select {
	case <-called:
		t.Fatal("unaccepted command reached the backend")
	default:
	}
	accepted.Store(true)
	tt.dispatch(0, wmCommand, StartAcceleration, 0)
	select {
	case id := <-called:
		if id != StartAcceleration {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("acceptance did not enable acceleration on the existing tray")
	}
}

func TestCanceledLogoffKeepsTrayAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tt := &tray{options: Options{EndSession: cancel}}
	if tt.dispatch(0, 0x11, 0, 0) != 1 {
		t.Fatal("logoff query rejected")
	}
	tt.dispatch(0, 0x16, 0, 0)
	if tt.closing {
		t.Fatal("canceled logoff stopped the app")
	}
	if ctx.Err() != nil {
		t.Fatal("canceled logoff canceled graceful waiting")
	}
}

func TestEndSessionCancelsGracefulWaiting(t *testing.T) {
	for _, alreadyClosing := range []bool{false, true} {
		t.Run(fmt.Sprintf("alreadyClosing=%t", alreadyClosing), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closes atomic.Int32
			done := make(chan struct{})
			go func() {
				// WM_ENDSESSION posts WM_QUIT. Retire this OS thread on return
				// so that its message queue cannot affect another test.
				runtime.LockOSThread()
				defer close(done)
				tt := &tray{shutdownDone: make(chan struct{}), options: Options{
					EndSession: cancel,
					Shutdown: func() {
						<-ctx.Done()
						closes.Add(1)
					},
				}}
				if alreadyClosing {
					tt.quit()
				}
				tt.dispatch(0, 0x16, 1, 0)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("session end did not release graceful waiting promptly")
			}
			if closes.Load() != 1 {
				t.Fatalf("cleanup ran %d times", closes.Load())
			}
		})
	}
}
