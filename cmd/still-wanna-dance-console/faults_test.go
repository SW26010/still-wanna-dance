package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/faults"
)

func TestFaultOpeningKeepsConnectedWebUI(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	view, err := console.NewRecovery(server.Listener.Addr().String(), desktop.Identity{}, "startup failed")
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = view
	server.Start()
	t.Cleanup(func() { view.Close(t.Context()); server.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/api/lifecycle", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var event struct{ Action string }
	if err := wsjson.Read(ctx, conn, &event); err != nil || event.Action != "running" {
		t.Fatal("WebUI did not connect", event, err)
	}
	center := faults.New()
	opened := make(chan string, 2)
	checked := make(chan bool, 2)
	watchCtx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() { stop(); <-done }()
	go func() {
		defer close(done)
		watchFaultOpenings(watchCtx, center, server.URL, func() bool {
			connected := view.WebUIConnected()
			checked <- connected
			return connected
		}, func(url string) error {
			opened <- url
			return nil
		}, func(err error) { t.Errorf("browser failed: %v", err) })
	}()
	center.Set("task", errors.New("task failed while settings page is open"))
	select {
	case connected := <-checked:
		if !connected {
			t.Fatal("fault did not observe the connected WebUI")
		}
	case <-ctx.Done():
		t.Fatal("fault was not consumed")
	}
	select {
	case url := <-opened:
		t.Fatalf("fault reopened the home page while WebUI was connected: %s", url)
	case <-time.After(100 * time.Millisecond):
	}
	// Closing the page must allow a later incident to open the WebUI again.
	_ = conn.CloseNow()
	for view.WebUIConnected() && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if view.WebUIConnected() {
		t.Fatal("closed WebUI remained connected")
	}
	center.Set("another-task", errors.New("new task failed after WebUI closed"))
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("fault did not open the WebUI after its last page closed")
	}
}

func TestFaultOpeningLeavesShutdownFailurePending(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	center := faults.New()
	problem := errors.New("shutdown failure")
	center.Set("shutdown", problem)
	cancel()
	watchFaultOpenings(ctx, center, "http://127.0.0.1:1", nil, func(string) error {
		t.Error("opened a listener after shutdown")
		return nil
	}, func(error) { t.Error("reported an unrelated browser failure") })
	if open, err := center.Take(); !open || !errors.Is(err, problem) {
		t.Fatal("shutdown fault was lost", open, err)
	}
}

func TestFaultOpeningInterruptedByShutdownIsRetained(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	center := faults.New()
	problem := errors.New("background failure")
	center.Set("storage", problem)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		watchFaultOpenings(ctx, center, "http://127.0.0.1:1", nil, func(string) error {
			close(started)
			<-release
			return nil
		}, func(error) { t.Error("unexpected browser failure") })
	}()
	<-started
	cancel()
	<-finished
	close(release)
	if open, err := center.Take(); !open || !errors.Is(err, problem) {
		t.Fatal("interrupted opening lost the fault", open, err)
	}
}

func TestFailureUIFallsBackWhenBrowserCannotOpen(t *testing.T) {
	cause, unavailable := errors.New("configuration invalid"), errors.New("browser unavailable")
	err := serveFailureUI(t.Context(), cause, desktop.Identity{}, "127.0.0.1:0", func(string) error { return nil }, func(string) error { return unavailable }, runRecoveryTrayFixture)
	if !errors.Is(err, cause) || !errors.Is(err, unavailable) {
		t.Fatalf("lost startup or browser failure: %v", err)
	}
}

func TestInstanceConflictSkipsRecovery(t *testing.T) {
	previous := openWebUI
	t.Cleanup(func() { openWebUI = previous })
	openWebUI = func(string) error {
		t.Error("instance conflict opened a recovery browser")
		return errors.New("unexpected browser opening")
	}
	conflict := &desktop.InstanceConflictError{Existing: desktop.Identity{Executable: "existing.exe", Config: "existing.json", PID: 123}}
	for _, cause := range []error{conflict, fmt.Errorf("startup: %w", conflict)} {
		if got := recoverApplication(cause); got != cause {
			t.Fatalf("conflict should go directly to native error handling: %v", got)
		}
	}
}

// Models the tray owner without touching the user's notification area.
func runRecoveryTrayFixture(ctx context.Context, options desktop.Options) error {
	options.Ready()
	<-ctx.Done()
	options.Shutdown()
	return nil
}

func TestFailureUIRequiresTray(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint("ready=", ready), func(t *testing.T) {
			unavailable := errors.New("fixture tray unavailable")
			var address string
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := serveFailureUI(ctx, errors.New("startup failed"), desktop.Identity{}, "127.0.0.1:0", func(value string) error {
				address = value
				return nil
			}, func(string) error {
				if !ready {
					t.Error("browser opened before tray was ready")
				}
				return nil
			}, func(ctx context.Context, options desktop.Options) error {
				if ready {
					options.Ready()
				}
				return unavailable
			})
			if !errors.Is(err, unavailable) {
				t.Fatalf("lost tray failure: %v", err)
			}
			response, err := http.Get("http://" + address + "/")
			if err == nil {
				response.Body.Close()
				t.Fatal("recovery server survived tray failure")
			}
		})
	}
}

func TestFailureUIRequiresPublishedInstance(t *testing.T) {
	unavailable := errors.New("fixture discovery unavailable")
	err := serveFailureUI(t.Context(), errors.New("startup failed"), desktop.Identity{}, "127.0.0.1:0", func(string) error { return unavailable }, func(string) error {
		t.Error("browser opened without instance registration")
		return nil
	}, func(context.Context, desktop.Options) error {
		t.Error("tray started without instance registration")
		return nil
	})
	if !errors.Is(err, unavailable) {
		t.Fatalf("lost registration failure: %v", err)
	}
}

func TestFailureUIUnexpectedTrayReturnStopsServer(t *testing.T) {
	err := serveFailureUI(t.Context(), errors.New("startup failed"), desktop.Identity{}, "127.0.0.1:0", func(string) error { return nil }, func(string) error { return nil }, func(context.Context, desktop.Options) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "托盘已意外关闭") {
		t.Fatalf("unexpected tray exit was not reported: %v", err)
	}
}

func TestFailureUITrayFollowsCommittedTheme(t *testing.T) {
	root := t.TempDir()
	identity := desktop.Identity{Executable: filepath.Join(root, "SWD.exe"), Config: filepath.Join(root, "config.json")}
	if err := os.WriteFile(identity.Config, []byte(`{"schemaVersion":8,"theme":"dark","videoStorageMode":"limited","videoBudgetBytes":0,"restoreHostsOnStopOrExit":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := serveFailureUI(ctx, errors.New("invalid video budget"), identity, "127.0.0.1:0", func(string) error { return nil }, func(string) error { return nil }, func(_ context.Context, options desktop.Options) error {
		defer options.RequestExit()
		page := func(theme string) []byte {
			t.Helper()
			response, err := http.Get(options.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), `"theme":"`+theme+`"`) {
				t.Fatal("initial page appearance differs from tray", response.StatusCode, err)
			}
			return body
		}
		if state := options.State(); state != (desktop.State{Unavailable: true, Theme: "dark"}) {
			t.Fatal("recovery tray did not load the saved theme", state)
		}
		token := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`).FindSubmatch(page("dark"))
		if len(token) != 2 {
			t.Fatal("missing recovery token")
		}
		for _, test := range []struct {
			body, theme string
			status      int
		}{
			{`{"theme":"light"}`, "dark", 400},
			{`{"theme":"light","videoBudgetBytes":1073741824}`, "light", 200},
		} {
			req, err := http.NewRequestWithContext(ctx, "POST", options.URL+"/api/settings", strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-SWD-Token", string(token[1]))
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatal("save result", response.StatusCode)
			}
			if state := options.State(); state != (desktop.State{Unavailable: true, Theme: test.theme}) {
				t.Fatal("tray did not follow the committed theme", state)
			}
			page(test.theme)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The child executes the production startup first, then uses an injected
// browser opener so the isolated test never opens a user's normal browser.
func TestApplicationRecoveryAfterInvalidConfiguration(t *testing.T) {
	for _, fixture := range []struct{ name, contents, reason string }{
		{"malformed", `{"schemaVersion":8,`, "读取程序配置"},
		{"contradictory", `{"schemaVersion":8,"autoOpenWebUI":false,"videoStorageMode":"limited","videoBudgetBytes":0}`, "视频存储预算"},
		{"fields", `{"schemaVersion":8,"autoOpenWebUI":false,"logLevel":"LOUD","theme":"unknown","proxyBackend":"unknown","downloadConcurrencySoftLimit":65,"requestRetentionDays":-1,"videoBudgetBytes":-1}`, "日志级别"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config.json")
			if err := os.WriteFile(config, []byte(fixture.contents), 0600); err != nil {
				t.Fatal(err)
			}
			output, err := os.Create(filepath.Join(root, "output.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			cmd := exec.Command(os.Args[0], "-config", config, "-no-tray", "-no-open", "-listen", "127.0.0.1:0")
			cmd.Env = append(os.Environ(), "SWD_RECOVERY_TEST_APP=1")
			cmd.Stdout, cmd.Stderr = output, output
			configureRestartCommand(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			defer cmd.Process.Kill()
			pattern := regexp.MustCompile(`RECOVERY_URL=(http://127\.0\.0\.1:\d+)`)
			var base string
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(output.Name())
				if match := pattern.FindSubmatch(data); len(match) == 2 {
					if !strings.Contains(string(data), fixture.reason) {
						t.Fatalf("unexpected startup failure: %s", data)
					}
					base = string(match[1])
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if base == "" {
				data, _ := os.ReadFile(output.Name())
				t.Fatalf("recovery UI did not start: %s", data)
			}
			if evidence := os.Getenv("SWD_RECOVERY_BROWSER_EVIDENCE"); evidence != "" {
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				browser := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "recovery-live.mjs"), base, filepath.Join(evidence, fixture.name), fixture.reason, fixture.name)
				out, err := browser.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("browser: %v\n%s", err, out)
				}
				t.Log(string(out))
			} else {
				response, err := http.Get(base + "/")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				token := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`).FindSubmatch(body)
				if response.StatusCode != 200 || len(token) != 2 {
					t.Fatalf("recovery page: %d %s", response.StatusCode, body)
				}
				request, _ := http.NewRequest("POST", base+"/api/exit", strings.NewReader(`{}`))
				request.Header.Set("X-SWD-Token", string(token[1]))
				response, err = http.DefaultClient.Do(request)
				if err != nil || response.StatusCode != 200 {
					t.Fatalf("recovery exit: %v %v", response, err)
				}
				response.Body.Close()
			}
			select {
			case err := <-wait:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("recovery process did not exit")
			}
			data, err := os.ReadFile(config)
			if fixture.name == "contradictory" && os.Getenv("SWD_RECOVERY_BROWSER_EVIDENCE") != "" {
				settings, readErr := console.ReadStartupSettings(config)
				if readErr != nil || settings.VideoBudgetBytes != 1073741824 {
					t.Fatal("repair was not persisted", readErr, settings.VideoBudgetBytes)
				}
			} else if fixture.name == "fields" && os.Getenv("SWD_RECOVERY_BROWSER_EVIDENCE") != "" {
				settings, readErr := console.ReadStartupSettings(config)
				if readErr != nil || settings.DownloadConcurrencySoftLimit != 8 || settings.VideoBudgetBytes != 0 {
					t.Fatal("field repair was not persisted", readErr)
				}
			} else if err != nil || string(data) != fixture.contents {
				t.Fatalf("configuration was changed: %q %v", data, err)
			}
		})
	}
}
