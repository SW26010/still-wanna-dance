package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/desktop"
)

func TestReadConsolePortIgnoresInvalidHints(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	path := apppaths.ForConfig(config).State("webui-port")
	if got := readConsolePort(config); got != 0 {
		t.Fatal("missing hint", got)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{`, `{}`, `{"port":0}`, `{"port":-1}`, `{"port":65536}`, `{"port":"1234"}`, `{"port":null}`, `{"port":1234}` + strings.Repeat(" ", 1024)} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if got := readConsolePort(config); got != 0 {
			t.Fatalf("accepted %q: %d", input, got)
		}
	}
}

func TestRestartPortFallsBackWhenOccupied(t *testing.T) {
	blocker, err := net.Listen("tcp4", desktop.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port
	listener, err := listenConsole(desktop.Address, filepath.Join(t.TempDir(), "config.json"), true, port)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if listener.Addr().(*net.TCPAddr).Port == port {
		t.Fatal("restart did not fall back from occupied port")
	}
}

// Each process uses an isolated configuration and unaccepted terms; it does not
// start the playback gateway, modify hosts, or contact public upstreams.
func TestApplicationPortReuseAndFallback(t *testing.T) {
	config := preparePortTestConfig(t)
	first := startPortTestApp(t, config)
	firstAddress := first.address
	assertSavedPort(t, config, firstAddress)
	first.stop(t)

	second := startPortTestApp(t, config)
	if second.address != firstAddress {
		t.Fatalf("did not reuse port: first %s, second %s", firstAddress, second.address)
	}
	second.stop(t)

	blocker, err := net.Listen("tcp4", firstAddress)
	if err != nil {
		t.Fatalf("could not occupy previous port: %v", err)
	}
	defer blocker.Close()
	third := startPortTestApp(t, config)
	if third.address == firstAddress {
		t.Fatal("did not fall back from occupied port", third.address)
	}
	fallbackAddress := third.address
	assertSavedPort(t, config, fallbackAddress)
	third.stop(t)

	// An explicitly requested address retains its previous fail-on-conflict
	// behavior, and a failed startup must not replace the last successful port.
	cmd := exec.Command(os.Args[0], "-config", config, "-no-tray", "-no-open", "-listen", firstAddress)
	cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
	configureRestartCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), firstAddress) {
		t.Fatalf("explicit occupied port did not fail: %v\n%s", err, output)
	}
	assertSavedPort(t, config, fallbackAddress)

	fourth := startPortTestApp(t, config)
	if fourth.address != fallbackAddress {
		t.Fatalf("did not reuse fallback port: got %s, want %s", fourth.address, fallbackAddress)
	}
	// Explicit selection also becomes the hint for a later default launch.
	manual, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer manual.Close()
	manualAddress := manual.Addr().String()
	fourth.stop(t)
	manual.Close()
	fifth := startPortTestApp(t, config, "-listen", manualAddress)
	if fifth.address != manualAddress {
		t.Fatalf("explicit port was not used: got %s, want %s", fifth.address, manualAddress)
	}
	assertSavedPort(t, config, manualAddress)
	fifth.stop(t)
	sixth := startPortTestApp(t, config)
	if sixth.address != manualAddress {
		t.Fatalf("did not reuse explicit port: got %s, want %s", sixth.address, manualAddress)
	}
	sixth.stop(t)
}

func TestApplicationStartsWhenPortHintCannotBeSaved(t *testing.T) {
	config := preparePortTestConfig(t)
	path := apppaths.ForConfig(config).State("webui-port")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	app := startPortTestApp(t, config)
	app.stop(t)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("unsafe port hint was changed", err)
	}
}

func preparePortTestConfig(t *testing.T) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.json")
	work, err := cacheproxy.OpenWorkDirectory(startupWorkForTest(t, config), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := console.New(config, "127.0.0.1:0", work)
	if err != nil {
		work.Close()
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		work.Close()
		t.Fatal(err)
	}
	if err := work.Close(); err != nil {
		t.Fatal(err)
	}
	return config
}

type portTestApp struct {
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	output  *os.File
	address string
	token   string
	client  *http.Client
}

func startPortTestApp(t *testing.T, config string, options ...string) *portTestApp {
	t.Helper()
	output, err := os.Create(filepath.Join(t.TempDir(), "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-config", config, "-no-tray", "-no-open"}, options...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
	cmd.Stdout, cmd.Stderr = output, output
	configureRestartCommand(cmd)
	if err := cmd.Start(); err != nil {
		output.Close()
		t.Fatal(err)
	}
	app := &portTestApp{cmd: cmd, done: make(chan struct{}), output: output, client: &http.Client{Timeout: time.Second}}
	go func() { app.err = cmd.Wait(); close(app.done) }()
	t.Cleanup(func() {
		select {
		case <-app.done:
		default:
			_ = cmd.Process.Kill()
			<-app.done
		}
		app.client.CloseIdleConnections()
		output.Close()
	})
	urlPattern := regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
	tokenPattern := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-app.done:
			data, _ := os.ReadFile(output.Name())
			t.Fatalf("application exited during startup: %v\n%s", app.err, data)
		default:
		}
		data, _ := os.ReadFile(output.Name())
		if url := urlPattern.Find(data); len(url) > 0 {
			app.address = strings.TrimPrefix(string(url), "http://")
			response, err := app.client.Get(string(url) + "/")
			if err == nil {
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if match := tokenPattern.FindSubmatch(body); len(match) == 2 {
					app.token = string(match[1])
					return app
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	data, _ := os.ReadFile(output.Name())
	t.Fatalf("application startup timed out:\n%s", data)
	return nil
}

func (app *portTestApp) stop(t *testing.T) {
	t.Helper()
	request, err := http.NewRequest("POST", "http://"+app.address+"/api/exit", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-SWD-Token", app.token)
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("exit failed", response.Status)
	}
	select {
	case <-app.done:
		if app.err != nil {
			t.Fatal("application exit failed", app.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("application did not exit")
	}
}

func assertSavedPort(t *testing.T, config, address string) {
	t.Helper()
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if got := readConsolePort(config); got != port {
		t.Fatalf("saved port %d, want %d", got, port)
	}
}
