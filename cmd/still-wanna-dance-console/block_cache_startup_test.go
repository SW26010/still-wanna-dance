package main

import (
	"encoding/json"
	"io"
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
)

func TestApplicationBlockCacheStartup(t *testing.T) {
	for _, name := range []string{"available", "offline", "offline-video", "offline-database", "default-offline-video", "default-offline-database"} {
		t.Run(name, func(t *testing.T) {
			defaultCache := strings.HasPrefix(name, "default-")
			mode := strings.TrimPrefix(name, "default-")
			available := mode != "offline"
			offlineData := mode == "offline-video" || mode == "offline-database"
			root := t.TempDir()
			config, cache := filepath.Join(root, "config.json"), filepath.Join(root, "selected-cache")
			settings := map[string]any{"schemaVersion": 8, "blockCacheDir": cache}
			if !offlineData {
				// Library creation is fixture setup, outside the child readiness
				// deadline for these block-workspace lifecycle checks.
				paths := apppaths.ForConfig(config)
				store, err := cacheproxy.CreateStore(filepath.Join(root, paths.DefaultDatabase))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(root, paths.DefaultVideo), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if defaultCache {
				cache = ""
				delete(settings, "blockCacheDir")
			}
			if offlineData {
				if runtime.GOOS != "windows" {
					t.Skip("the unavailable drive fixture requires Windows")
				}
				var offline string
				for _, letter := range "ZYXWQVUTSR" {
					candidate := string(letter) + `:\`
					if _, err := os.Stat(candidate); os.IsNotExist(err) {
						offline = candidate
						break
					}
				}
				if offline == "" {
					t.Skip("no unused drive letter available")
				}
				if mode == "offline-video" {
					database := filepath.Join(root, "storage.sqlite")
					store, err := cacheproxy.CreateStore(database)
					if err != nil {
						t.Fatal(err)
					}
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					settings["databasePath"], settings["videoDir"] = database, filepath.Join(offline, "videos")
				} else {
					video := filepath.Join(root, "videos")
					if err := os.Mkdir(video, 0700); err != nil {
						t.Fatal(err)
					}
					settings["databasePath"], settings["videoDir"] = filepath.Join(offline, "storage.sqlite"), video
				}
			}
			data, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, data, 0600); err != nil {
				t.Fatal(err)
			}
			identity, err := desktop.NewIdentity(config)
			if err != nil {
				t.Fatal(err)
			}
			workPath := apppaths.WorkForCacheRoot(identity.Executable, identity.Config, cache)
			if !defaultCache && filepath.Dir(workPath) != cache {
				t.Fatal("selected cache did not contain the instance workspace directly", workPath)
			}
			block, unknown := filepath.Join(workPath, "download-block-123.part"), filepath.Join(workPath, "keep.txt")
			if defaultCache {
				t.Cleanup(func() {
					for _, path := range []string{block, unknown, filepath.Join(workPath, ".lock")} {
						if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
							t.Errorf("remove fixture file: %v", err)
						}
					}
					if err := os.Remove(workPath); err != nil && !os.IsNotExist(err) {
						t.Errorf("remove fixture workspace: %v", err)
					}
				})
			}
			if available {
				if err := os.MkdirAll(workPath, 0700); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{block, unknown} {
					if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			defaultWork := apppaths.WorkForConfig(identity.Executable, identity.Config)
			if _, err := os.Stat(defaultWork); !defaultCache && !os.IsNotExist(err) {
				t.Fatal("test requires a fresh default workspace", err)
			}
			outputPath := filepath.Join(root, "startup.log")
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
			wait := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				wait <- cmd.Wait()
			}()
			defer func() {
				_ = cmd.Process.Kill()
				<-done
			}()
			urlPattern := regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
			tokenPattern := regexp.MustCompile(`name="swd-token"\s+content="([a-f0-9]+)"`)
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			var address, token string
			started := time.Now()
			waitReady := func(deadline time.Time) {
				for time.Now().Before(deadline) {
					b, _ := os.ReadFile(outputPath)
					address = string(urlPattern.Find(b))
					if address != "" {
						response, err := client.Get(address + "/")
						if err == nil {
							body, _ := io.ReadAll(response.Body)
							response.Body.Close()
							if match := tokenPattern.FindSubmatch(body); len(match) == 2 {
								token = string(match[1])
								return
							}
						}
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			waitReady(started.Add(15 * time.Second))
			phases := func() {
				logs, err := applog.Read(t.Context(), apppaths.ForConfig(config).LogDir, applog.Query{})
				if err != nil {
					t.Logf("[DEBUG-rc1-startup] logs: %v", err)
					return
				}
				for _, entry := range logs.Entries {
					var fields map[string]any
					if json.Unmarshal([]byte(entry.Raw), &fields) == nil {
						t.Logf("[DEBUG-rc1-startup] phase time=%v event=%v", fields["time"], fields["msg"])
					}
				}
			}
			if token == "" {
				t.Logf("[DEBUG-rc1-startup] original 15s deadline missed after %v", time.Since(started))
				phases()
				waitReady(started.Add(45 * time.Second))
				t.Logf("[DEBUG-rc1-startup] late ready=%t elapsed=%v", token != "", time.Since(started))
				phases()
				b, _ := os.ReadFile(outputPath)
				t.Fatalf("application did not start: %s", b)
			}
			t.Logf("[DEBUG-rc1-startup] ready elapsed=%v", time.Since(started))
			phases()
			if available {
				if _, err := os.Stat(block); offlineData && err != nil || !offlineData && !os.IsNotExist(err) {
					t.Fatal("startup cleaned or retained a block unexpectedly", err)
				}
				if data, err := os.ReadFile(unknown); err != nil || string(data) != "fixture" {
					t.Fatal("startup changed an unknown file", err)
				}
				if other, err := cacheproxy.OpenWorkDirectory(workPath, nil); err == nil {
					other.Close()
					t.Fatal("running application did not hold the selected workspace")
				}
			} else {
				if _, err := os.Stat(cache); !os.IsNotExist(err) {
					t.Fatal("offline cache root was recreated", err)
				}
				if _, err := os.Stat(defaultWork); !os.IsNotExist(err) {
					t.Fatal("unavailable selection silently fell back to default", err)
				}
			}
			if mode == "offline" || offlineData {
				logs, err := applog.Read(t.Context(), apppaths.ForConfig(config).LogDir, applog.Query{})
				if err != nil {
					t.Fatal(err)
				}
				unavailable, deferred := false, false
				for _, entry := range logs.Entries {
					unavailable = unavailable || strings.Contains(entry.Raw, "block_cache_unavailable")
					deferred = deferred || strings.Contains(entry.Raw, "block_cache_cleanup_deferred")
				}
				if mode == "offline" && !unavailable || offlineData && (unavailable || !deferred) {
					t.Fatal("startup misattributed the cache or data-path failure", logs.Entries)
				}
			}
			request, _ := http.NewRequest("POST", address+"/api/exit", strings.NewReader(`{}`))
			request.Header.Set("X-SWD-Token", token)
			response, err := client.Do(request)
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatal("application could not exit", err)
			}
			response.Body.Close()
			select {
			case err := <-wait:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("application did not exit")
			}
		})
	}
}
