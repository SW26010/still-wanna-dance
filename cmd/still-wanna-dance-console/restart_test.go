package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestMain(m *testing.M) {
	if os.Getenv("SWD_RESTART_TEST_APP") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Run the real entrypoint in a child process, including its config lease,
// listener, shutdown, replacement process and fresh session token. No consent
// is granted, so neither process probes public upstreams or starts downloads.
func TestApplicationRestartReleasesConfigAndKeepsAddress(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "configuration with spaces.json")
	outputPath := filepath.Join(root, "output.log")
	output, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd := exec.Command(os.Args[0], "-config", config, "-listen", "127.0.0.1:0", "-no-tray", "-no-open")
	cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
	cmd.Stdout, cmd.Stderr = output, output
	configureRestartCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	var address, token string
	urlPattern := regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
	tokenPattern := regexp.MustCompile(`const token="([a-f0-9]+)"`)
	poll := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		b, _ := os.ReadFile(outputPath)
		t.Fatalf("application transition timed out: %s", b)
	}
	readToken := func() string {
		response, err := client.Get(address + "/")
		if err != nil {
			return ""
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		match := tokenPattern.FindSubmatch(body)
		if len(match) != 2 {
			return ""
		}
		return string(match[1])
	}
	post := func(action, session string) (int, error) {
		r, _ := http.NewRequest("POST", address+"/api/"+action, strings.NewReader(`{}`))
		r.Header.Set("X-StepStash-Token", session)
		response, err := client.Do(r)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		return response.StatusCode, nil
	}
	poll(func() bool {
		b, _ := os.ReadFile(outputPath)
		address = string(urlPattern.Find(b))
		if address == "" {
			return false
		}
		token = readToken()
		return token != ""
	})
	defer func() {
		if current := readToken(); current != "" {
			_, _ = post("exit", current)
		}
	}()
	if lease, err := desktop.LockConfig(config); err == nil {
		lease.Close()
		t.Fatal("running process did not hold configuration lease")
	}
	if code, err := post("restart", token); err != nil || code != 200 {
		t.Fatal(code, err)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("old process did not exit")
	}
	var next string
	poll(func() bool { next = readToken(); return next != "" && next != token })
	if code, err := post("exit", token); err != nil || code != 403 {
		t.Fatal("old session accepted", code, err)
	}
	if lease, err := desktop.LockConfig(config); err == nil {
		lease.Close()
		t.Fatal("replacement did not acquire configuration lease")
	}
	if code, err := post("exit", next); err != nil || code != 200 {
		t.Fatal(code, err)
	}
	poll(func() bool {
		lease, err := desktop.LockConfig(config)
		if err != nil {
			return false
		}
		lease.Close()
		return true
	})
}
