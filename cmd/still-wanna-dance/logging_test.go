package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCLILoggingProcess(t *testing.T) {
	if os.Getenv("SWD_CLI_LOG_TEST") != "1" {
		return
	}
	os.Args = []string{os.Args[0], "-log-level", os.Getenv("SWD_CLI_LOG_LEVEL"), "-create-database"}
	main() // Missing database must fail without opening stores or the network.
	os.Exit(0)
}

func TestCLILogLevelAndStartupErrors(t *testing.T) {
	for _, level := range []string{"info", "debug", "warn", "error", "off", "invalid"} {
		t.Run(level, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCLILoggingProcess$")
			cmd.Env = append(os.Environ(), "SWD_CLI_LOG_TEST=1", "SWD_CLI_LOG_LEVEL="+level)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("expected invalid arguments to fail")
			}
			if level == "off" {
				if len(output) != 0 {
					t.Fatalf("off emitted logs: %s", output)
				}
				return
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output), &record); err != nil {
				t.Fatalf("non-JSON CLI failure: %v: %s", err, output)
			}
			if record["level"] != "ERROR" || record["session_id"] == nil || record["pid"] == nil || !strings.HasSuffix(record["time"].(string), "Z") {
				t.Fatal(record)
			}
		})
	}
}
