package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPathDialogHelperSkipsApplicationStartup(t *testing.T) {
	for _, input := range []string{"", `{"mode":"invalid"}`} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-path-dialog")
		// Reuse the real-main subprocess entry from the application tests.
		cmd.Env = append(os.Environ(), "SWD_RESTART_TEST_APP=1")
		cmd.Dir = t.TempDir()
		cmd.Stdin = strings.NewReader(input)
		var output, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &stderr
		err := cmd.Run()
		cancel()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("helper did not return its failure: %v\n%s", err, &stderr)
		}
		if output.Len() != 0 || !strings.Contains(stderr.String(), "无效的路径选择") {
			t.Fatalf("unexpected helper output: stdout=%q stderr=%q", &output, &stderr)
		}
		files, err := os.ReadDir(cmd.Dir)
		if err != nil || len(files) != 0 {
			t.Fatal("helper created application files", files, err)
		}
	}
}
