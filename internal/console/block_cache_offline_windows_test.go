package console

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/workfiles"
)

func unusedDrive(t *testing.T) string {
	t.Helper()
	for _, letter := range "ZYXWQVUTSR" {
		root := fmt.Sprintf("%c:\\", letter)
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return root
		}
	}
	t.Skip("no unused drive letter available")
	return ""
}

func TestBlockCacheSelectionWithOfflineLibraryVolume(t *testing.T) {
	offline := unusedDrive(t)
	for _, resource := range []string{"database", "video", "both"} {
		t.Run(resource, func(t *testing.T) {
			c := testConsole(t)
			settings := c.settings
			if resource == "database" || resource == "both" {
				settings.DatabasePath = filepath.Join(offline, "missing", "storage.sqlite")
			}
			if resource == "video" || resource == "both" {
				settings.VideoDir = filepath.Join(offline, "missing", "videos")
			}
			c.settings, c.savedSettings = settings, settings
			settings.BlockCacheDir = t.TempDir()
			if err := c.save(settings); err != nil {
				t.Fatalf("unavailable %s prevented selecting a separate cache: %v", resource, err)
			}
			if c.savedSettings.BlockCacheDir != settings.BlockCacheDir {
				t.Fatal("cache selection was not saved")
			}
		})
	}
}

func TestDefaultBlockCacheChecksOfflineLibraryAndAliases(t *testing.T) {
	offline := unusedDrive(t)
	c := testConsole(t)
	c.identity.Executable = filepath.Join(t.TempDir(), "SWD.exe")
	for _, resource := range []string{"database", "video"} {
		t.Run(resource, func(t *testing.T) {
			settings := c.settings
			if resource == "database" {
				settings.DatabasePath = filepath.Join(offline, "storage.sqlite")
			} else {
				settings.VideoDir = filepath.Join(offline, "videos")
			}
			inspection, err := InspectWorkLocation(c.identity.Executable, c.configPath, settings)
			if err != nil || inspection.Unresolved == nil {
				t.Fatalf("default workspace did not defer an offline %s: inspection=%+v, err=%v", resource, inspection, err)
			}
		})
	}
	work := blockWorkPath(c.identity.Executable, c.configPath, c.settings)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "video-alias")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, filepath.Dir(work)).CombinedOutput(); err != nil {
		t.Fatalf("create isolated directory junction: %s: %v", output, err)
	}
	settings := c.settings
	settings.DatabasePath = filepath.Join(offline, "storage.sqlite")
	settings.VideoDir = alias
	if _, err := InspectWorkLocation(c.identity.Executable, c.configPath, settings); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatal("default workspace accepted an overlapping video alias while the database was offline", err)
	}
}

func TestBlockCacheSelectionChecksExistingWorkspacePermissions(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		existing  string
		denied    string
		denyFiles bool
		wantError bool
	}{
		{"root", "root", "0x00000006", true, true},
		{"workspace", "workspace", "0x00000006", true, true},
		{"root-cannot-create-subdirectory", "root", "0x00000004", false, true},
		{"workspace-cannot-create-subdirectory", "workspace", "0x00000004", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			root := t.TempDir()
			next := c.settings
			next.BlockCacheDir = root
			work := blockWorkPath(c.identity.Executable, c.configPath, next)
			dir := filepath.Dir(work)
			if tc.existing == "workspace" {
				dir = work
			} else if tc.existing == "root" {
				dir = root
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			setDACL := func(sddl string) error {
				sd, err := windows.SecurityDescriptorFromString(sddl)
				if err != nil {
					return err
				}
				dacl, _, err := sd.DACL()
				if err != nil {
					return err
				}
				return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
			}
			sid := user.User.Sid.String()
			if err := setDACL("D:P(D;;" + tc.denied + ";;;" + sid + ")(A;;FA;;;" + sid + ")"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := setDACL("D:P(A;;FA;;;" + sid + ")"); err != nil {
					t.Errorf("restore fixture access: %v", err)
				}
			})
			if err := workfiles.Probe(root); (err != nil) != (tc.existing == "root" && tc.denyFiles) {
				t.Fatalf("fixture root file-write permission differs from expectation: %v", err)
			}
			if err := workfiles.Probe(dir); (err != nil) != tc.denyFiles {
				t.Fatalf("fixture file-write permission differs from expectation: %v", err)
			}
			err := c.save(next)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "不可写") {
					t.Fatal("saved a cache selection whose existing workspace cannot be prepared", err)
				}
				if c.savedSettings.BlockCacheDir != "" {
					t.Fatal("failed selection changed the saved cache location")
				}
			} else if err != nil || c.savedSettings.BlockCacheDir != root {
				t.Fatal("unneeded subdirectory permission prevented saving an existing writable workspace", err)
			}
		})
	}
}

func TestBlockCacheDefersCleanupUntilLibraryAliasesAreVerified(t *testing.T) {
	offline := unusedDrive(t)
	c := testConsole(t)
	originalVideo := c.settings.VideoDir
	root := t.TempDir()
	settings := c.settings
	settings.BlockCacheDir = root
	settings.VideoDir = filepath.Join(offline, "videos")
	workPath := blockWorkPath(c.identity.Executable, c.configPath, settings)
	if err := os.MkdirAll(workPath, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(workPath, "download-block-123.part")
	if err := os.WriteFile(block, []byte("old block"), 0600); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectWorkLocation(c.identity.Executable, c.configPath, settings)
	if err != nil || inspection.Unresolved == nil {
		t.Fatal("offline video path was not identified as unresolved", inspection, err)
	}
	work, err := cacheproxy.OpenWorkDirectoryDeferred(workPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close(); work.Close() }()
	c.mu.Lock()
	c.work, c.settings = work, settings
	c.mu.Unlock()
	if err := saveConfigSettingsWithCleanup(c.configPath, settings, &root); err != nil {
		t.Fatal(err)
	}
	CleanupPendingBlockCaches(c.identity.Executable, c.configPath, settings, work, nil)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] == nil {
		t.Fatal("deferred current cleanup was incorrectly acknowledged", err)
	}
	var actions []string
	err = c.enableAcceleration(func() HostsStatus { return HostsStatus{} }, func(action string) error {
		actions = append(actions, action)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "完整视频目录") || len(actions) != 0 {
		t.Fatal("unresolved cleanup attempted game connection", err, actions)
	}
	if _, err := c.ensureEngineLocked(); err == nil || !strings.Contains(err.Error(), "完整视频目录") {
		t.Fatal("offline video was not reported as a data-path problem", err)
	}
	if _, err := os.Stat(block); err != nil || !work.CleanupPending() {
		t.Fatal("unverified work was cleaned", err)
	}
	alias := filepath.Join(t.TempDir(), "video-alias")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, root).CombinedOutput(); err != nil {
		t.Fatalf("create isolated directory junction: %s: %v", output, err)
	}
	c.mu.Lock()
	c.settings.VideoDir = alias
	settings = c.settings
	c.mu.Unlock()
	// An unresolved database must not hide a resolvable overlapping video alias.
	withOfflineDatabase := settings
	withOfflineDatabase.DatabasePath = filepath.Join(offline, "storage.sqlite")
	if _, err := InspectWorkLocation(c.identity.Executable, c.configPath, withOfflineDatabase); err == nil {
		t.Fatal("offline database hid an overlapping video alias")
	}
	if _, err := c.ensureEngineLocked(); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatal("overlapping video alias was accepted", err)
	}
	if _, err := os.Stat(block); err != nil || !work.CleanupPending() {
		t.Fatal("alias conflict deleted a block", err)
	}
	c.mu.Lock()
	c.settings.VideoDir = originalVideo
	c.mu.Unlock()
	if _, err := c.ensureEngineLocked(); err != nil {
		t.Fatal("safe resources did not permit cleanup and engine startup", err)
	}
	if _, err := os.Stat(block); !os.IsNotExist(err) || work.CleanupPending() {
		t.Fatal("verified startup did not clean the old block", err)
	}
}

func TestPreviousBlockCacheWaitsForOfflineLibraryPath(t *testing.T) {
	offline := unusedDrive(t)
	c := testConsole(t)
	previousRoot := t.TempDir()
	previous := c.settings
	previous.BlockCacheDir = previousRoot
	oldWork := blockWorkPath(c.identity.Executable, c.configPath, previous)
	if err := os.MkdirAll(oldWork, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(oldWork, "download-block-456.part")
	if err := os.WriteFile(block, []byte("old block"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigSettingsWithCleanup(c.configPath, c.settings, &previousRoot); err != nil {
		t.Fatal(err)
	}
	unavailable := c.settings
	unavailable.VideoDir = filepath.Join(offline, "videos")
	CleanupPendingBlockCaches(c.identity.Executable, c.configPath, unavailable, c.work, nil)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] == nil {
		t.Fatal("unverified cleanup was incorrectly acknowledged", err)
	}
	if _, err := os.Stat(block); err != nil {
		t.Fatal("offline library allowed unsafe cleanup", err)
	}
	CleanupPendingBlockCaches(c.identity.Executable, c.configPath, c.settings, c.work, nil)
	_, fields, err = readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] != nil {
		t.Fatal("verified old workspace cleanup stayed pending", err)
	}
	if _, err := os.Stat(block); !os.IsNotExist(err) {
		t.Fatal("verified old workspace was not cleaned", err)
	}
}

func TestPreviousDefaultBlockCacheWaitsForOfflineLibraryPath(t *testing.T) {
	offline := unusedDrive(t)
	c := testConsole(t)
	c.identity.Executable = filepath.Join(t.TempDir(), "SWD.exe")
	previousRoot := ""
	oldWork := blockWorkPath(c.identity.Executable, c.configPath, c.settings)
	if err := os.MkdirAll(oldWork, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(oldWork, "download-block-456.part")
	if err := os.WriteFile(block, []byte("old block"), 0600); err != nil {
		t.Fatal(err)
	}
	current := c.settings
	current.BlockCacheDir = t.TempDir()
	if err := saveConfigSettingsWithCleanup(c.configPath, current, &previousRoot); err != nil {
		t.Fatal(err)
	}
	unavailable := current
	unavailable.VideoDir = filepath.Join(offline, "videos")
	CleanupPendingBlockCaches(c.identity.Executable, c.configPath, unavailable, c.work, nil)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] == nil {
		t.Fatal("unverified default cleanup was incorrectly acknowledged", err)
	}
	if _, err := os.Stat(block); err != nil {
		t.Fatal("offline library allowed default workspace cleanup", err)
	}
	CleanupPendingBlockCaches(c.identity.Executable, c.configPath, current, c.work, nil)
	_, fields, err = readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] != nil {
		t.Fatal("verified old default workspace cleanup stayed pending", err)
	}
	if _, err := os.Stat(block); !os.IsNotExist(err) {
		t.Fatal("verified old default workspace was not cleaned", err)
	}
}

func TestDefaultBlockCacheResumesDeferredCleanup(t *testing.T) {
	offline := unusedDrive(t)
	c := testConsole(t)
	c.identity.Executable = filepath.Join(t.TempDir(), "SWD.exe")
	settings := c.settings
	settings.VideoDir = filepath.Join(offline, "videos")
	workPath := blockWorkPath(c.identity.Executable, c.configPath, settings)
	if err := os.MkdirAll(workPath, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(workPath, "download-block-123.part")
	if err := os.WriteFile(block, []byte("old block"), 0600); err != nil {
		t.Fatal(err)
	}
	work, err := cacheproxy.OpenWorkDirectoryDeferred(workPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close(); work.Close() }()
	originalVideo := c.settings.VideoDir
	c.mu.Lock()
	c.work, c.settings = work, settings
	c.mu.Unlock()
	request := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	request.Header.Set("X-SWD-Token", c.token)
	response := httptest.NewRecorder()
	c.ServeHTTP(response, request)
	var status struct {
		WorkError string `json:"workError"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != 200 || status.WorkError != "" {
		t.Fatal("offline video was reported as a default workspace failure", status, err)
	}
	if _, err := c.ensureEngineLocked(); err == nil || !strings.Contains(err.Error(), "完整视频目录") {
		t.Fatal("offline video did not prevent deferred cleanup", err)
	}
	if _, err := os.Stat(block); err != nil || !work.CleanupPending() {
		t.Fatal("default workspace was cleaned while the video was offline", err)
	}
	c.mu.Lock()
	c.settings.VideoDir = originalVideo
	c.mu.Unlock()
	if _, err := c.ensureEngineLocked(); err != nil {
		t.Fatal("restored video path did not permit engine startup", err)
	}
	if _, err := os.Stat(block); !os.IsNotExist(err) || work.CleanupPending() {
		t.Fatal("restored video path did not permit cleanup", err)
	}
}
