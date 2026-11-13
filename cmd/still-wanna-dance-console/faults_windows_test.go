package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/legal"
	"still-wanna-dance/internal/workfiles"
)

// Uses a private config and a real tray without claiming the user's desktop
// instance. Only discovery publication is replaced; no business services run.
func TestRecoveryTrayExitsAfterBrowserCloses(t *testing.T) {
	evidence := os.Getenv("SWD_RECOVERY_EXIT_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_RECOVERY_EXIT_BROWSER_EVIDENCE for real tray/browser close integration")
	}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"schemaVersion":8,`), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := desktop.NewIdentity(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	done, browserDone := make(chan error, 1), make(chan error, 1)
	var address string
	go func() {
		done <- serveFailureUI(ctx, errors.New("fixture configuration invalid"), identity, "127.0.0.1:0", func(value string) error {
			address = value
			return nil
		}, func(url string) error {
			go func() {
				cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "recovery-exit-live.mjs"), url, evidence)
				output, err := cmd.CombinedOutput()
				if err != nil {
					err = fmt.Errorf("browser close: %w\n%s", err, output)
				}
				browserDone <- err
			}()
			return nil
		}, desktop.Run)
	}()
	defer cancel()
	select {
	case err := <-browserDone:
		if err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("recovery ended before browser closed: %v", err)
	}
	closed := time.Now()
	if response, err := http.Get("http://" + address + "/"); err != nil {
		t.Fatal("recovery did not retain refresh grace", err)
	} else {
		response.Body.Close()
	}
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil {
			t.Fatal("recovery did not exit automatically", err, ctx.Err())
		}
	case <-ctx.Done():
		cancel()
		<-done
		t.Fatal("recovery tray remained after grace expired")
	}
	if elapsed := time.Since(closed); elapsed < 58*time.Second || elapsed > 65*time.Second {
		t.Fatalf("unexpected post-close lifetime: %s", elapsed)
	}
	if response, err := http.Get("http://" + address + "/"); err == nil {
		response.Body.Close()
		t.Fatal("recovery server remained after tray returned")
	}
	lease, err := desktop.LockConfig(config)
	if err != nil {
		t.Fatal("recovery retained config lease", err)
	}
	_ = lease.Close()
	t.Log("Real tray and recovery server closed after 60 seconds without pages; config lease released.")
}

func TestFaultOpeningNavigationBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_FAULT_NAVIGATION_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_FAULT_NAVIGATION_BROWSER_EVIDENCE for real fault/navigation integration")
	}
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{"schemaVersion":8,"autoOpenWebUI":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeAcceptedTerms(t, config)
	work, err := cacheproxy.OpenWorkDirectory(filepath.Join(root, "work"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	ui := httptest.NewUnstartedServer(nil)
	c, err := console.New(config, ui.Listener.Addr().String(), work)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Fail an actual settings write without changing the last saved config.
	if err := os.WriteFile(filepath.Join(root, workfiles.TempName(config)), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	center := faults.New()
	c.SetFaultCenter(center)
	var checks, openings atomic.Int32
	ui.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test/fault-openings" {
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks.Load(), "openings": openings.Load(), "connected": c.WebUIConnected()})
			return
		}
		c.ServeHTTP(w, r)
	})
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		watchFaultOpenings(ctx, center, ui.URL, func() bool {
			connected := c.WebUIConnected()
			checks.Add(1)
			return connected
		}, func(string) error { openings.Add(1); return nil }, func(err error) { t.Errorf("browser failed: %v", err) })
	}()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "fault-navigation-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

func requireUnusedDesktop(t *testing.T) {
	t.Helper()
	if os.Getenv("SWD_RECOVERY_DESKTOP_EVIDENCE") == "" {
		t.Skip("set SWD_RECOVERY_DESKTOP_EVIDENCE for isolated Windows desktop verification")
	}
	name, err := windows.UTF16PtrFromString(desktop.MutexName)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return
	}
	if err != nil {
		t.Skipf("cannot check the existing desktop instance: %v", err)
	}
	defer windows.CloseHandle(handle)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	switch state {
	case uint32(windows.WAIT_TIMEOUT):
		t.Skip("another SWD desktop instance is running")
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
		if err := windows.ReleaseMutex(handle); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unexpected desktop mutex state: %d", state)
	}
}

func writeAcceptedTerms(t *testing.T, config string) {
	t.Helper()
	plain, err := json.Marshal(map[string]any{"purpose": "swd-terms-receipt-v1", "version": legal.Version, "sha256": legal.Hash(), "acceptedAt": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	cipher := append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...)
	data, err := json.Marshal(map[string]any{"format": 2, "protection": "dpapi-current-user", "receipts": [][]byte{cipher}})
	if err != nil {
		t.Fatal(err)
	}
	path := apppaths.ForConfig(config).Terms
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// Induces a real browser-open failure and dismisses only the dialog owned by
// this test process; no existing browser or application window is touched.
func TestWindowsDesktopNativeFallback(t *testing.T) {
	testDesktopStartupDialog(t, false)
}

func TestWindowsDesktopInstanceConflict(t *testing.T) {
	testDesktopStartupDialog(t, true)
}

func testDesktopStartupDialog(t *testing.T, conflict bool) {
	t.Helper()
	requireUnusedDesktop(t)
	root := t.TempDir()
	cache := filepath.Join(root, "local-app-data")
	if conflict {
		t.Setenv("LOCALAPPDATA", cache)
		identity, err := desktop.NewIdentity(filepath.Join(root, "existing.json"))
		if err != nil {
			t.Fatal(err)
		}
		owner, err := desktop.AcquireOrActivate(t.Context(), identity, false)
		if err != nil || owner == nil {
			t.Fatalf("could not own isolated desktop: %v", err)
		}
		defer owner.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(identity)
		}))
		defer server.Close()
		if err := owner.Publish(strings.TrimPrefix(server.URL, "http://")); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{"schemaVersion":8,`), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(os.Args[0], "-config", config, "-no-open", "-listen", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "SWD_DESKTOP_RECOVERY_TEST_APP=1", "SWD_DESKTOP_RECOVERY_OPEN_FAIL=1", "LOCALAPPDATA="+cache)
	cmd.Stdout, cmd.Stderr = log, log
	configureRestartCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumerate := user32.NewProc("EnumWindows")
	windowPID := user32.NewProc("GetWindowThreadProcessId")
	className := user32.NewProc("GetClassNameW")
	postMessage := user32.NewProc("PostMessageW")
	var dialog uintptr
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		windowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == uint32(cmd.Process.Pid) {
			var class [64]uint16
			className.Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
			if windows.UTF16ToString(class[:]) == "#32770" {
				dialog = hwnd
			}
		}
		return 1
	})
	deadline := time.Now().Add(15 * time.Second)
	for dialog == 0 && time.Now().Before(deadline) {
		enumerate.Call(callback, 0)
		if dialog == 0 {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if dialog == 0 {
		output, _ := os.ReadFile(log.Name())
		t.Fatalf("native fallback dialog did not appear: %s", output)
	}
	reader := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", filepath.Join("..", "..", "scripts", "read-dialog-text.ps1"), "-ProcessId", fmt.Sprint(cmd.Process.Pid))
	text, err := reader.CombinedOutput()
	if err != nil {
		t.Fatalf("read native dialog: %v\n%s", err, text)
	}
	message := string(text)
	if conflict {
		output, _ := os.ReadFile(log.Name())
		if !strings.Contains(message, "本次启动已取消") || !strings.Contains(message, "existing.json") || strings.Contains(string(output), "DESKTOP_OPEN_URL=") {
			t.Errorf("instance conflict opened recovery or lost its reason: %q %s", message, output)
		}
	} else if !strings.Contains(message, "读取程序配置") || !strings.Contains(message, "fixture browser unavailable") {
		t.Fatalf("fallback lost either failure: %q", message)
	}
	// TDM_CLICK_BUTTON / IDCLOSE, directed to this child only.
	if ok, _, err := postMessage.Call(dialog, 0x400+102, 8, 0); ok == 0 {
		t.Fatalf("could not dismiss isolated dialog: %v", err)
	}
	select {
	case err := <-done:
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("unexpected process result: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("native fallback did not close")
	}
}

// Exercises the real Windows desktop entry, mutex, discovery and tray. Browser
// launching is replaced only to avoid opening a tab in the user's profile.
func TestWindowsDesktopRecovery(t *testing.T) {
	requireUnusedDesktop(t)
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	data := `{"schemaVersion":8,"autoOpenWebUI":false,"videoStorageMode":"limited","videoBudgetBytes":0}`
	if err := os.WriteFile(config, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cache := filepath.Join(root, "local-app-data")
	cmd := exec.Command(os.Args[0], "-config", config, "-no-open", "-listen", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "SWD_DESKTOP_RECOVERY_TEST_APP=1", "LOCALAPPDATA="+cache)
	cmd.Stdout, cmd.Stderr = log, log
	configureRestartCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	pattern := regexp.MustCompile(`DESKTOP_OPEN_URL=(http://127\.0\.0\.1:\d+)`)
	var base string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		output, _ := os.ReadFile(log.Name())
		if match := pattern.FindSubmatch(output); len(match) == 2 {
			base = string(match[1])
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if base == "" {
		output, _ := os.ReadFile(log.Name())
		t.Fatalf("desktop did not open recovery: %s", output)
	}
	if entries, _ := filepath.Glob(filepath.Join(cache, "SWD", "console-session-*.json")); len(entries) != 1 {
		t.Fatal("recovery did not own and publish its isolated desktop identity", entries)
	}
	response, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	token := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`).FindSubmatch(body)
	if len(token) != 2 {
		t.Fatal("recovery page has no token")
	}
	request, _ := http.NewRequest("POST", base+"/api/exit", strings.NewReader(`{}`))
	request.Header.Set("X-SWD-Token", string(token[1]))
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response, err)
	}
	response.Body.Close()
	select {
	case err := <-done:
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("startup failure should retain exit status 1: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("desktop recovery did not exit")
	}
	output, _ := os.ReadFile(log.Name())
	if strings.Contains(string(output), "托盘不可用") {
		t.Fatalf("Windows tray could not start: %s", output)
	}
	if entries, _ := filepath.Glob(filepath.Join(cache, "SWD", "console-session-*.json")); len(entries) != 0 {
		t.Fatal("desktop discovery record survived exit", entries)
	}
	if contents, err := os.ReadFile(config); err != nil || string(contents) != data {
		t.Fatal("configuration was changed", err)
	}
}

// Existing consent and disabled auto-open ensure the only opening comes from
// the real startup work-directory fault, rather than the consent flow.
func TestWindowsDesktopFaultOverridesDisabledAutoOpen(t *testing.T) {
	requireUnusedDesktop(t)
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	database, video := filepath.Join(root, "library.sqlite"), filepath.Join(root, "videos")
	store, err := cacheproxy.CreateStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(video, 0700); err != nil {
		t.Fatal(err)
	}
	blockedCache := filepath.Join(root, "cache-is-a-file")
	if err := os.WriteFile(blockedCache, nil, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"schemaVersion": 8, "autoOpenWebUI": false, "autoAccelerate": false,
		"databasePath": database, "videoDir": video, "blockCacheDir": blockedCache,
		"upstreamMode": "proxy", "proxyBackend": "external-socks5", "socks5Address": "127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	writeAcceptedTerms(t, config)
	log, err := os.Create(filepath.Join(root, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(os.Args[0], "-config", config, "-listen", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "SWD_DESKTOP_RECOVERY_TEST_APP=1", "LOCALAPPDATA="+filepath.Join(root, "local-app-data"))
	cmd.Stdout, cmd.Stderr = log, log
	configureRestartCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	pattern := regexp.MustCompile(`DESKTOP_OPEN_URL=(http://127\.0\.0\.1:\d+)`)
	var base string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		output, _ := os.ReadFile(log.Name())
		if match := pattern.FindSubmatch(output); len(match) == 2 {
			base = string(match[1])
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if base == "" {
		output, _ := os.ReadFile(log.Name())
		t.Fatalf("work fault did not open the WebUI: %s", output)
	}
	response, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	token := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`).FindSubmatch(body)
	if len(token) != 2 || strings.Contains(string(body), `"termsGate":{`) {
		t.Fatal("page lacked a token or unexpectedly requested consent", string(body))
	}
	response, err = http.Get(base + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		WorkError string `json:"workError"`
		Settings  struct {
			AutoOpen bool `json:"autoOpenWebUI"`
		} `json:"settings"`
	}
	err = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || status.WorkError == "" || status.Settings.AutoOpen {
		t.Fatalf("did not use accepted terms and disabled auto-open: %+v %v", status, err)
	}
	request, _ := http.NewRequest("POST", base+"/api/exit", strings.NewReader(`{}`))
	request.Header.Set("X-SWD-Token", string(token[1]))
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response, err)
	}
	response.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("desktop did not exit")
	}
}
