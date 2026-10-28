package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
)

func startupWorkForTest(t *testing.T, config string) string {
	t.Helper()
	exe, err := desktop.CanonicalPath(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	config, err = desktop.CanonicalPath(config)
	if err != nil {
		t.Fatal(err)
	}
	workPath := apppaths.WorkForConfig(exe, config)
	if err := os.MkdirAll(workPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(workPath) })
	return workPath
}

func TestApplicationCleansBlocksBeforeStartupFailure(t *testing.T) {
	for _, failure := range []string{"blocked_logs", "invalid_config"} {
		t.Run(failure, func(t *testing.T) {
			config, err := desktop.CanonicalPath(filepath.Join(t.TempDir(), "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			contents := `{"schemaVersion":7,"logLevel":"info"}`
			if failure == "invalid_config" {
				contents = `{`
			}
			if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "blocked_logs" {
				if err := os.WriteFile(filepath.Dir(apppaths.ForConfig(config).Log), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			workPath := startupWorkForTest(t, config)
			block := filepath.Join(workPath, "download-block-123456789.part")
			unknown := filepath.Join(workPath, "keep.txt")
			for _, name := range []string{block, unknown} {
				if err := os.WriteFile(name, []byte("leftover"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-config", config, "-listen", "127.0.0.1:0", "-no-tray", "-no-open")
			cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
			configureRestartCommand(cmd)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("expected startup failure")
			}
			message := "无法创建程序日志"
			if failure == "invalid_config" {
				message = "读取程序配置"
			}
			if !strings.Contains(string(output), message) {
				t.Fatalf("unexpected startup failure: %v\n%s", err, output)
			}
			if _, err := os.Stat(block); !os.IsNotExist(err) {
				t.Fatalf("startup failure left a disposable block: %v\n%s", err, output)
			}
			if data, err := os.ReadFile(unknown); err != nil || string(data) != "leftover" {
				t.Fatalf("startup cleanup changed an unknown file: %v", err)
			}
			work, err := cacheproxy.OpenWorkDirectory(workPath, nil)
			if err != nil {
				t.Fatalf("startup failure retained the work lease: %v", err)
			}
			if err := work.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Exercise the real startup sink and final error path without starting a
// Console, accepting terms or contacting upstreams. An occupied loopback port
// fails after logging/work setup, so Off must reach that failure unchanged.
func TestApplicationLogLevelAtStartup(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, level := range []string{"info", "warn", "error", "debug", "off", "invalid"} {
		t.Run(level, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(config, []byte(`{"schemaVersion":7,"logLevel":"`+level+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			workPath := startupWorkForTest(t, config)
			block := filepath.Join(workPath, "download-block-123456789.part")
			if err := os.WriteFile(block, []byte("leftover"), 0600); err != nil {
				t.Fatal(err)
			}
			// Recognized names with unexpected types are preserved with a warning.
			skipped := filepath.Join(workPath, "download-block-987654321.part")
			if err := os.Mkdir(skipped, 0700); err != nil {
				t.Fatal(err)
			}
			logPath := apppaths.ForConfig(config).Log
			if level == "off" {
				if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
					t.Fatal(err)
				}
				for _, suffix := range []string{"", ".1"} {
					if err := os.WriteFile(logPath+suffix, []byte("preserve existing log"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			cmd := exec.Command(os.Args[0], "-config", config, "-listen", listener.Addr().String(), "-no-tray", "-no-open")
			cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
			configureRestartCommand(cmd)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("expected startup failure")
			}
			if _, err := os.Stat(block); !os.IsNotExist(err) {
				t.Fatalf("block was not cleaned at level %s: %v", level, err)
			}
			if info, err := os.Stat(skipped); err != nil || !info.IsDir() {
				t.Fatalf("cleanup removed an unexpected file type: %v", err)
			}
			wantInfo := level == "info" || level == "debug" || level == "invalid"
			wantWarn := wantInfo || level == "warn"
			if bytes.Contains(output, []byte("temporary_cleanup_removed")) != wantInfo ||
				bytes.Contains(output, []byte("temporary_cleanup_skipped")) != wantWarn {
				t.Fatalf("cleanup ignored level %s: %s", level, output)
			}
			if level == "off" {
				for _, suffix := range []string{"", ".1"} {
					data, err := os.ReadFile(logPath + suffix)
					if err != nil || string(data) != "preserve existing log" {
						t.Fatal("off modified existing logs", err)
					}
				}
				// A blocked log directory must not stop a subsequent Off startup.
				other := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(other, []byte(`{"schemaVersion":7,"logLevel":"off"}`), 0600); err != nil {
					t.Fatal(err)
				}
				blocked := filepath.Dir(apppaths.ForConfig(other).Log)
				if err := os.MkdirAll(filepath.Dir(blocked), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(blocked, nil, 0600); err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(os.Args[0], "-config", other, "-listen", listener.Addr().String(), "-no-tray", "-no-open")
				cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
				configureRestartCommand(cmd)
				blockedOutput, err := cmd.CombinedOutput()
				if err == nil || !bytes.Equal(output, blockedOutput) {
					t.Fatalf("off accessed logs: %v\n%s\n%s", err, output, blockedOutput)
				}
				return
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err, string(output))
			}
			started, failed := false, false
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err, string(line))
				}
				if record["session_id"] == nil || record["pid"] == nil || !strings.HasSuffix(record["time"].(string), "Z") {
					t.Fatal(record)
				}
				started = started || record["msg"] == "application_starting"
				failed = failed || record["msg"] == "application_failed"
			}
			if !failed || started != (level == "info" || level == "debug" || level == "invalid") {
				t.Fatalf("incorrect level filter: %s", data)
			}
		})
	}
}
