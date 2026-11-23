package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/testlog"
	"still-wanna-dance/internal/workfiles"
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

func TestApplicationContinuesWithOptionalStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, settings, wantLogError, logBlock string
	}{
		{"blocked logs", `"theme":"invalid","vrcxImportDismissed":12`, "无法写入程序日志目录", "directory"},
		{"unwritable current segment", `"logLevel":"info"`, "无法写入程序日志目录", "current segment"},
		{"locked old segment", `"logBudgetBytes":1048576`, "无法写入程序日志目录", "old segment"},
		{"invalid budget", `"logBudgetBytes":"invalid"`, "logBudgetBytes", ""},
		{"disabled with invalid budget", `"logLevel":"off","logBudgetBytes":0`, "logBudgetBytes", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(config, []byte(`{"schemaVersion":8,`+tc.settings+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			startupWorkForTest(t, config)
			p := apppaths.ForConfig(config)
			blocked := filepath.Join(p.StateDir, workfiles.TempName(p.State("batch-last-attempt")), "keep")
			if err := os.MkdirAll(filepath.Dir(blocked), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			switch tc.logBlock {
			case "directory":
				if err := os.WriteFile(p.LogDir, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "current segment", "old segment":
				if runtime.GOOS != "windows" {
					t.Skip("Windows file permissions and sharing")
				}
				if err := os.Mkdir(p.LogDir, 0700); err != nil {
					t.Fatal(err)
				}
				name := "20010101.log"
				if tc.logBlock == "current segment" {
					name = time.Now().Format("20060102") + ".log"
				}
				segment := filepath.Join(p.LogDir, name)
				if err := os.WriteFile(segment, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if tc.logBlock == "current segment" {
					if err := os.Chmod(segment, 0400); err != nil {
						t.Fatal(err)
					}
					defer os.Chmod(segment, 0600)
				} else {
					hold, err := os.Open(segment)
					if err != nil {
						t.Fatal(err)
					}
					defer hold.Close()
				}
				t.Cleanup(func() {
					if b, err := os.ReadFile(segment); err != nil || string(b) != "keep" {
						t.Error("startup changed the disabled log segment", err)
					}
				})
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-config", config, "-listen", "127.0.0.1:0", "-no-tray", "-no-open")
			cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			configureRestartCommand(cmd)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					cancel()
					_ = cmd.Wait()
				}
			}()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() {
				_ = cmd.Wait()
				t.Fatal("missing WebUI address", scanner.Err(), stderr.String())
			}
			address := regexp.MustCompile(`http://127\.0\.0\.1:\d+`).FindString(scanner.Text())
			if address == "" {
				t.Fatal("missing WebUI address", scanner.Text())
			}
			client := &http.Client{Timeout: 3 * time.Second}
			defer client.CloseIdleConnections()
			request := func(method, path, token string) []byte {
				t.Helper()
				r, err := http.NewRequestWithContext(ctx, method, address+path, strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("X-SWD-Token", token)
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != 200 {
					t.Fatal(path, response.StatusCode, string(body), err)
				}
				return body
			}
			home := request("GET", "/", "")
			token := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`).FindSubmatch(home)
			if len(token) != 2 {
				t.Fatal("normal WebUI did not provide a management token")
			}
			if !bytes.Contains(home, []byte(`"theme":"system"`)) {
				t.Fatal("normal WebUI did not use the system theme")
			}
			if b, err := os.ReadFile(blocked); err != nil || string(b) != "keep" {
				t.Fatal("startup changed an unsafe slot", err)
			}
			request("POST", "/api/exit", string(token[1]))
			if err := cmd.Wait(); err != nil {
				t.Fatal(err, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantLogError) {
				t.Fatal("startup did not report the log fault", stderr.String())
			}
		})
	}
}

func TestApplicationCleansBlocksBeforeStartupFailure(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, failure := range []string{"blocked_logs", "invalid_config"} {
		t.Run(failure, func(t *testing.T) {
			config, err := desktop.CanonicalPath(filepath.Join(t.TempDir(), "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			contents := `{"schemaVersion":8,"logLevel":"info"}`
			if failure == "invalid_config" {
				contents = `{`
			}
			if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "blocked_logs" {
				if err := os.WriteFile(apppaths.ForConfig(config).LogDir, nil, 0600); err != nil {
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
			cmd := exec.CommandContext(ctx, os.Args[0], "-config", config, "-listen", listener.Addr().String(), "-no-tray", "-no-open")
			cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
			configureRestartCommand(cmd)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("expected startup failure")
			}
			message := listener.Addr().String()
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

func TestApplicationLogBudgetAtStartup(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listen := listener.Addr().String()
	for _, tc := range []struct {
		name, config, failure string
		oldFiles, retained    int
		writeLog              bool
	}{
		{"missing", "", listen, 24, 19, true},
		{"default", `{"schemaVersion":8}`, listen, 2, 2, true},
		{"custom", `{"schemaVersion":8,"logBudgetBytes":2097152}`, listen, 2, 1, true},
		{"off", `{"schemaVersion":8,"logLevel":"off","logBudgetBytes":2097152}`, listen, 2, 2, false},
		{"invalid_budget", `{"schemaVersion":8,"logBudgetBytes":0}`, listen, 24, 24, false},
		{"off_invalid_budget", `{"schemaVersion":8,"logLevel":"off","logBudgetBytes":0}`, listen, 24, 24, false},
		{"malformed_json", `{"schemaVersion":8,"logBudgetBytes":64 MiB}`, "读取程序配置", 24, 24, false},
		{"invalid_theme", `{"schemaVersion":8,"logBudgetBytes":1048576,"theme":"invalid"}`, listen, 24, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			if tc.config != "" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			startupWorkForTest(t, config)
			logDir := apppaths.ForConfig(config).LogDir
			if err := os.MkdirAll(logDir, 0700); err != nil {
				t.Fatal(err)
			}
			paths := make([]string, tc.oldFiles)
			data := make([]byte, applog.MaxBytes)
			for i := range paths {
				paths[i] = filepath.Join(logDir, fmt.Sprintf("20200101T000000Z-%d.log", i+1))
				data[0] = byte(i)
				if err := os.WriteFile(paths[i], data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-config", config, "-listen", listen, "-no-tray", "-no-open")
			cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
			configureRestartCommand(cmd)
			if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), tc.failure) {
				t.Fatalf("unexpected startup result: %v\n%s", err, output)
			}
			wantFiles := tc.retained
			if tc.writeLog {
				wantFiles++
			}
			if entries, err := os.ReadDir(logDir); err != nil || len(entries) != wantFiles {
				t.Errorf("found %d log files, want %d: %v", len(entries), wantFiles, err)
			}
			for i, path := range paths {
				if i < tc.oldFiles-tc.retained {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("old segment %d was not removed: %v", i+1, err)
					}
					continue
				}
				data[0] = byte(i)
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
					t.Fatalf("retained segment %d was lost or modified: %v", i+1, err)
				}
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
			if err := os.WriteFile(config, []byte(`{"schemaVersion":8,"logLevel":"`+level+`"}`), 0600); err != nil {
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
			logDir := apppaths.ForConfig(config).LogDir
			betaNames := []string{"app.log.3", "app.log.2", "app.log.1", "app.log"}
			betaRecord := []byte("{\"time\":\"2026-11-02T00:00:00Z\",\"level\":\"INFO\",\"msg\":\"beta4_fixture\",\"pid\":123,\"session_id\":\"beta4\"}\n")
			if level == "info" {
				if err := os.MkdirAll(logDir, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range betaNames {
					if err := os.WriteFile(filepath.Join(logDir, name), betaRecord, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			existingNames := []string{"app.log", "app.log.1", "20261103T000000Z.log", "2026-11-03T00-00-00.000000000Z.log"}
			if level == "off" {
				if err := os.MkdirAll(logDir, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range existingNames {
					if err := os.WriteFile(filepath.Join(logDir, name), []byte("preserve existing log"), 0600); err != nil {
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
			if level == "invalid" {
				if !bytes.Contains(output, []byte("程序日志级别必须为")) {
					t.Fatalf("missing configuration error on stderr: %s", output)
				}
				if _, err := os.Stat(logDir); !os.IsNotExist(err) {
					t.Fatalf("invalid log level accessed the log directory: %v", err)
				}
				return
			}
			if level == "off" {
				for _, name := range existingNames {
					data, err := os.ReadFile(filepath.Join(logDir, name))
					if err != nil || string(data) != "preserve existing log" {
						t.Fatal("off modified existing logs", err)
					}
				}
				entries, err := os.ReadDir(logDir)
				if err != nil || len(entries) != len(existingNames) {
					t.Fatal("off created or cleaned log files", entries, err)
				}
				// A blocked log directory must not stop a subsequent Off startup.
				other := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(other, []byte(`{"schemaVersion":8,"logLevel":"off"}`), 0600); err != nil {
					t.Fatal(err)
				}
				blocked := apppaths.ForConfig(other).LogDir
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
			logs, err := testlog.Read(logDir)
			if err != nil {
				t.Fatal(err, string(output))
			}
			if level == "info" {
				if len(logs.Files) != len(betaNames)+1 {
					t.Fatalf("beta4 upgrade did not retain old logs and create a new segment: %v", logs.Files)
				}
				for i, name := range betaNames {
					data, err := os.ReadFile(filepath.Join(logDir, name))
					if logs.Files[i] != name || err != nil || !bytes.Equal(data, betaRecord) {
						t.Fatalf("beta4 log was reordered or modified: %s %v", name, err)
					}
				}
			}
			started, failed := false, false
			betaRecords := 0
			for _, entry := range logs.Entries {
				record := entry.Fields
				_, timeErr := time.Parse(time.RFC3339Nano, record["time"].(string))
				if record["session_id"] == nil || record["pid"] == nil || timeErr != nil {
					t.Fatal(record)
				}
				started = started || record["event_name"] == "application_starting"
				failed = failed || record["event_name"] == "application_failed"
				if record["event_name"] == "beta4_fixture" {
					betaRecords++
				}
			}
			if level == "info" && betaRecords != len(betaNames) {
				t.Fatalf("beta4 records were not readable: %d", betaRecords)
			}
			if !failed || started != (level == "info" || level == "debug") {
				t.Fatalf("incorrect level filter: %+v", logs)
			}
		})
	}
}
