package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/apppaths"
)

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
