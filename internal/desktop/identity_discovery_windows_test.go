package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDiscoveryInstallationConflicts(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := NewIdentity(config)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(id) }))
	defer s.Close()
	path := filepath.Join(root, "session.json")
	owner := &Instance{lease: fakeLease{}, path: path, identity: id}
	defer owner.Close()
	if err := owner.Publish(strings.TrimPrefix(s.URL, "http://")); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"same", "case", "relative", "hardlink", "hardlink-directory", "config", "executable"} {
		t.Run(change, func(t *testing.T) {
			request := id
			switch change {
			case "case":
				request.Config = strings.ToUpper(config)
			case "relative":
				t.Chdir(root)
				request, err = NewIdentity("config.json")
				if err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				request.Config = filepath.Join(root, "alias.json")
				if err := os.Link(config, request.Config); err != nil {
					t.Fatal(err)
				}
			case "config":
				request.Config = filepath.Join(root, "other.json")
			case "hardlink-directory":
				request.Config = filepath.Join(t.TempDir(), "config.json")
				if err := os.Link(config, request.Config); err != nil {
					t.Fatal(err)
				}
			case "executable":
				request.Executable = filepath.Join(root, "other", "SWD.exe")
			}
			for _, open := range []bool{true, false} {
				opens := 0
				lease, err := acquireOrActivate(context.Background(), path, request, func() (Lease, error) { return nil, nil }, open, func(url string) error {
					opens++
					if url != s.URL {
						t.Error(url)
					}
					return nil
				})
				conflict := change == "config" || change == "executable" || strings.HasPrefix(change, "hardlink")
				if lease != nil || (err != nil) != conflict || opens != boolInt(open && !conflict) {
					t.Fatalf("lease=%v err=%v opens=%d", lease, err, opens)
				}
				if conflict && (!strings.Contains(err.Error(), id.Executable) || !strings.Contains(err.Error(), id.Config)) {
					t.Fatal("missing conflict ownership", err)
				}
				if got := readInstanceRecord(path); got == nil || got.Identity != id {
					t.Fatal("existing owner disturbed")
				}
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDiscoveryRejectsReusedPIDAndForgedIdentity(t *testing.T) {
	id, err := NewIdentity(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(id) }))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "session.json")
	for _, field := range []string{"pid", "started", "executable", "config", "app", "protocol"} {
		t.Run(field, func(t *testing.T) {
			forged := id
			switch field {
			case "pid":
				forged.PID++
			case "started":
				forged.Started = "1"
			case "executable":
				forged.Executable = filepath.Join(t.TempDir(), "SWD.exe")
			case "config":
				forged.Config = filepath.Join(t.TempDir(), "other.json")
			case "app":
				forged.App = "other"
			case "protocol":
				forged.Protocol++
			}
			b, err := json.Marshal(instanceRecord{Address: strings.TrimPrefix(s.URL, "http://"), Identity: forged})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if _, err := acquireOrActivate(ctx, path, id, func() (Lease, error) { return nil, nil }, true, func(string) error { t.Fatal("activated forged owner"); return nil }); err == nil {
				t.Fatal("forged record accepted")
			}
		})
	}
}

func TestSWDSessionDiscoveryScope(t *testing.T) {
	path, err := instancePath()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(root, "SWD") || !strings.HasPrefix(filepath.Base(path), "console-session-") || MutexName != `Local\SWD.Desktop.v1` {
		t.Fatal("wrong desktop scope", path, MutexName)
	}
}

func TestConfigLockThroughWindowsJunction(t *testing.T) {
	root := t.TempDir()
	real, alias := filepath.Join(root, "real directory"), filepath.Join(root, "alias directory")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, real)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("junction: %s: %v", output, err)
	}
	a, b := filepath.Join(real, "data", "config.json"), filepath.Join(alias, "data", "config.json")
	if !sameConfigPath(a, b) {
		t.Fatal("junction configuration identity rejected")
	}
	if !samePath(a, b) {
		ac, ae := CanonicalPath(a)
		bc, be := CanonicalPath(b)
		t.Fatalf("junction to uncreated configuration rejected: %q (%v), %q (%v)", ac, ae, bc, be)
	}
	first, err := LockConfig(a)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := LockConfig(b); err == nil {
		second.Close()
		t.Fatal("junction bypassed config lock")
	}
}
