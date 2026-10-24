package console

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/cacheproxy"
)

// Opt-in integration against actual delivered executables. All data and DPAPI
// receipts are synthetic test fixtures, never copied from a user's installation.
// The standalone Portable smoke continues to exercise the unaccepted terms gate.
func TestPortableExecutableUpgrade(t *testing.T) {
	before, after := os.Getenv("SWD_UPGRADE_BEFORE_EXE"), os.Getenv("SWD_UPGRADE_AFTER_EXE")
	if before == "" || after == "" {
		t.Skip("set SWD_UPGRADE_BEFORE_EXE and SWD_UPGRADE_AFTER_EXE to two extracted Portable executables")
	}
	identities := make([]buildinfo.Info, 2)
	for i, exe := range []string{before, after} {
		data, err := exec.Command(exe, "-build-info").Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &identities[i]); err != nil {
			t.Fatal(err)
		}
		binary, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("version=%s revision=%s dirty=%t exeSHA256=%x", identities[i].Version, identities[i].Revision, identities[i].Dirty, sha256.Sum256(binary))
	}
	if identities[0].Version == identities[1].Version {
		t.Fatal("two distinct delivery versions are required")
	}
	for _, layout := range []string{"default", "external", "custom-config"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			install := filepath.Join(root, "SWD")
			if err := os.MkdirAll(install, 0700); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(install, "SWD.exe")
			config := filepath.Join(install, "data", "config.json")
			if layout == "custom-config" {
				config = filepath.Join(root, "profiles", "dance.json")
			}
			c, err := New(config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			s := c.settings
			s.AutoAccelerate, s.QueueWithGateway, s.VideoStorageMode = false, false, "unlimited"
			s.UpstreamMode, s.SOCKS5Address = "socks5", "127.0.0.1:1"
			s.SOCKS5Username, s.SOCKS5Password = "upgrade-test", "synthetic-upgrade-secret"
			if layout == "external" {
				s.StorageDir = filepath.Join(root, "external library")
			}
			if err := c.save(s); err != nil {
				t.Fatal(err)
			}
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
			if _, err := c.proxyIdentity(s); err != nil {
				t.Fatal(err)
			}
			finished := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
			if err := c.writeSnapshot("batch", s.StorageDir, Batch{Total: 7, Checked: 7, Hits: 7, Finished: finished}); err != nil {
				t.Fatal(err)
			}
			files := c.files()
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := fixtureCacheConfig()
			cfg.StorageDir = s.StorageDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			media := []byte("synthetic media payload: preserve every byte across a binary replacement")
			md5sum := md5.Sum(media)
			key := hex.EncodeToString(md5sum[:])
			source := filepath.Join(root, "fixture.mp4")
			if err := os.WriteFile(source, media, 0600); err != nil {
				t.Fatal(err)
			}
			receipt, err := engine.ImportVideo(context.Background(), source, map[string]bool{key: true})
			if err != nil {
				engine.Close()
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			if receipt == nil {
				t.Fatal("fixture import did not publish a resource")
			}
			unknown := filepath.Join(install, "personal-note.txt")
			if err := os.WriteFile(unknown, []byte("user owned"), 0600); err != nil {
				t.Fatal(err)
			}
			preserved := []string{config, files.Terms, files.ChannelKey, files.State("batch"), filepath.Join(s.StorageDir, "storage.sqlite"), receipt.Destination, unknown}
			hashes := map[string][32]byte{}
			for _, file := range preserved {
				hashes[file] = upgradeFileHash(t, file)
			}
			var previousLog []byte
			for i, binary := range []string{before, after} {
				content, err := os.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(exe, content, 0700); err != nil {
					t.Fatal(err)
				}
				probePortableUpgrade(t, exe, config, root, s.StorageDir, identities[i].Version, key)
				for _, file := range preserved {
					if upgradeFileHash(t, file) != hashes[file] {
						t.Fatalf("upgrade changed %s", filepath.Base(file))
					}
				}
				log, err := os.ReadFile(files.Log)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(log, previousLog) {
					t.Fatal("restart truncated application log")
				}
				previousLog = log
			}
			// Reopen the same real SQLite library after both binaries have read it.
			engine, err = cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func upgradeFileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func probePortableUpgrade(t *testing.T, exe, config, cwd, storage, version, key string) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cmd := exec.Command(exe, "-no-tray", "-no-open", "-listen", address, "-config", config)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostic, &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Log(diagnostic.String())
		}
	}()
	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + address
	var body []byte
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("portable exited before ready: %v", err)
		default:
		}
		response, err := client.Get(base + "/api/identity")
		if err != nil {
			continue
		}
		body, err = io.ReadAll(response.Body)
		response.Body.Close()
		var identity struct {
			PID int `json:"pid"`
		}
		if err == nil && json.Unmarshal(body, &identity) == nil && identity.PID == cmd.Process.Pid {
			break
		}
		body = nil
	}
	if body == nil {
		t.Fatal("portable startup timed out")
	}
	get := func(route, token string) []byte {
		t.Helper()
		req, _ := http.NewRequest("GET", base+route, nil)
		req.Header.Set("X-SWD-Token", token)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("%s: status=%d err=%v", route, response.StatusCode, err)
		}
		return b
	}
	page := get("/about", "")
	match := regexp.MustCompile(`"token":"([^"]+)"`).FindSubmatch(page)
	if len(match) != 2 || !bytes.Contains(page, []byte(fmt.Sprintf(`"version":%q`, version))) {
		t.Fatal("wrong portable bootstrap")
	}
	token := string(match[1])
	var status struct {
		Running               bool
		ActiveSettings        Settings
		SOCKS5PasswordSet     bool
		SOCKS5CredentialError string
		LastBatch             Batch
	}
	if err := json.Unmarshal(get("/api/status", token), &status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.ActiveSettings.AutoAccelerate || status.ActiveSettings.QueueWithGateway || status.ActiveSettings.StorageDir != storage || !status.SOCKS5PasswordSet || status.SOCKS5CredentialError != "" || status.LastBatch.Hits != 7 {
		t.Fatal("runtime did not retain settings, decrypted credentials or task snapshot")
	}
	if !bytes.Contains(get("/api/cache", token), []byte(key)) {
		t.Fatal("runtime could not read imported SQLite resource")
	}
	req, _ := http.NewRequest("POST", base+"/api/exit", bytes.NewBufferString("{}"))
	req.Header.Set("X-SWD-Token", token)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("portable did not exit")
	}
}
