package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

var fixtureNodes []map[string]any

func TestMain(m *testing.M) { os.Exit(runTests(m)) }
func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "mihomo-fixture-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", exe, "./testfixture")
	if b, e := build.CombinedOutput(); e != nil {
		panic(string(b) + e.Error())
	}
	manifest := filepath.Join(dir, "manifest.json")
	server := exec.CommandContext(ctx, exe, manifest)
	log, err := os.Create(filepath.Join(dir, "fixture.log"))
	if err != nil {
		panic(err)
	}
	defer log.Close()
	server.Stdout, server.Stderr = log, log
	if err := server.Start(); err != nil {
		panic(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if b, e := os.ReadFile(manifest); e == nil {
			var result struct {
				Nodes []map[string]any `json:"nodes"`
			}
			if json.Unmarshal(b, &result) == nil {
				fixtureNodes = result.Nodes
				break
			}
		}
		if time.Now().After(deadline) {
			panic("fixture readiness timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	code := m.Run()
	if code != 0 {
		if data, err := os.ReadFile(log.Name()); err == nil {
			if len(data) > 8192 {
				data = data[len(data)-8192:]
			}
			_, _ = os.Stderr.Write(data)
		}
	}
	return code
}

func httpThrough(tr http.RoundTripper, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "http://business.invalid"+path, nil)
	resp, err := (&http.Client{Transport: tr}).Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 || string(b) != "mihomo-embedded-business-response\n" {
		return errors.New("wrong egress response")
	}
	return nil
}
