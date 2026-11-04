package main

import (
	"context"
	"errors"
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
	view, err := console.NewRecovery(server.Listener.Addr().String(), desktop.Identity{})
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
	err := serveFailureUI(t.Context(), cause, desktop.Identity{}, "127.0.0.1:0", nil, func(string) error { return unavailable }, nil)
	if !errors.Is(err, cause) || !errors.Is(err, unavailable) {
		t.Fatalf("lost startup or browser failure: %v", err)
	}
}

func TestFailureUIRemainsAvailableWithoutTray(t *testing.T) {
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveFailureUI(t.Context(), errors.New("startup failed"), desktop.Identity{}, "127.0.0.1:0", &desktop.Instance{}, func(url string) error {
			opened <- url
			return nil
		}, func(context.Context, desktop.Options) error { return errors.New("tray unavailable") })
	}()
	var base string
	select {
	case base = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery UI was not opened")
	}
	response, err := http.Get(base + "/")
	if err != nil {
		t.Fatal("tray failure closed the recovery UI", err)
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
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not exit")
	}
}

// The child executes the production startup first, then uses an injected
// browser opener so the isolated test never opens a user's normal browser.
func TestApplicationRecoveryAfterInvalidConfiguration(t *testing.T) {
	for _, fixture := range []struct{ name, contents, reason string }{
		{"malformed", `{"schemaVersion":8,`, "读取程序配置"},
		{"contradictory", `{"schemaVersion":8,"autoOpenWebUI":false,"videoStorageMode":"limited","videoBudgetBytes":0}`, "视频存储预算"},
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
				browser := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "recovery-live.mjs"), base, filepath.Join(evidence, fixture.name))
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
			if err != nil || string(data) != fixture.contents {
				t.Fatalf("configuration was changed: %q %v", data, err)
			}
		})
	}
}
