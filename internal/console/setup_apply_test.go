package console

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestPlan(c *Console) setupPlan {
	s := c.savedSettings
	version, _ := c.setupSettingsVersion(s)
	return setupPlan{ID: "test-application", BaseVersion: version, StorageDir: s.StorageDir,
		VideoStorageMode: s.VideoStorageMode, AggressiveCacheReclamation: s.AggressiveCacheReclamation, MaxCacheBytes: s.MaxCacheBytes, UpstreamMode: s.UpstreamMode, SOCKS5Address: s.SOCKS5Address, SOCKS5User: s.SOCKS5Username}
}

func TestSetupVideoStorageModes(t *testing.T) {
	for _, mode := range []string{"none", "limited", "unlimited"} {
		c := testConsole(t)
		p := setupTestPlan(c)
		p.VideoStorageMode, p.MaxCacheBytes = mode, 5<<30
		validated, err := c.validateSetupPlan(p)
		if err != nil || validated.VideoStorageMode != mode || validated.MaxCacheBytes != 5<<30 {
			t.Fatalf("validate: %+v %v", validated, err)
		}
		a, err := c.applySetupPlan(p)
		if err != nil || !a.ConfigSaved || c.savedSettings.VideoStorageMode != mode || c.savedSettings.MaxCacheBytes != 5<<30 {
			t.Fatalf("apply: %+v %v", a, err)
		}
		if c.settings.VideoStorageMode != "none" || c.settings.MaxCacheBytes != 0 {
			t.Fatal("wizard changed active settings before restart")
		}
	}
}

func TestSetupValidationNeverSavesOrStarts(t *testing.T) {
	c := testConsole(t)
	before, _ := os.ReadFile(c.configPath)
	original := c.savedSettings
	p := setupTestPlan(c)
	p.StorageDir = filepath.Join(t.TempDir(), "new-library")
	p.MaxCacheBytes = 12 << 30
	p.VideoStorageMode = "limited"
	if _, err := c.validateSetupPlan(p); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(before, after) || c.savedSettings != original || c.settings != original || c.service != nil || c.importStatus.Running {
		t.Fatal("validation mutated business state")
	}
	if _, err := os.Stat(p.StorageDir); !os.IsNotExist(err) {
		t.Fatal("validation created a media library", err)
	}
	p.UpstreamMode, p.SOCKS5Address, p.Page = "socks5", "bad-address", "proxy"
	if _, err := c.validateSetupPlan(p); err == nil {
		t.Fatal("invalid proxy accepted")
	}
	p.Page, p.ImportSource = "import", t.TempDir()
	if _, err := c.validateSetupPlan(p); err != nil {
		t.Fatal("import page depends on unrelated draft fields", err)
	}
}

func TestSetupApplyRestartsBeforeImportAndResumesOnce(t *testing.T) {
	c := testConsole(t)
	p := setupTestPlan(c)
	p.VideoStorageMode = "limited"
	p.StorageDir, p.ImportSource, p.MaxCacheBytes = filepath.Join(t.TempDir(), "new-library"), t.TempDir(), 25<<30
	old := c.settings
	a, err := c.applySetupPlan(p)
	if err != nil || a.Phase != "restart" || !a.ConfigSaved || c.settings != old || c.service != nil {
		t.Fatalf("apply: %+v %v", a, err)
	}
	if c.savedSettings.StorageDir != p.StorageDir || c.savedSettings.PrepareAheadSeconds != old.PrepareAheadSeconds {
		t.Fatal("incorrect core/advanced settings")
	}
	// Repeating the same approval, even with its stale base, returns its receipt.
	if again, err := c.applySetupPlan(p); err != nil || again.ID != a.ID || again.Phase != "restart" {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	calls := 0
	start := func(source string) error {
		calls++
		if source != p.ImportSource || loaded.settings.StorageDir != p.StorageDir {
			t.Fatal("import did not bind to confirmed source and new target")
		}
		return nil
	}
	loaded.resumeSetupApplication(start)
	loaded.resumeSetupApplication(start)
	if calls != 1 || loaded.setupApplication.Phase != "completed" || !loaded.setupApplication.ImportStarted {
		t.Fatalf("continuation: %d %+v", calls, loaded.setupApplication)
	}
}

func TestSetupApplyStaleConfigurationAndBusyTasksAreRejected(t *testing.T) {
	c := testConsole(t)
	p := setupTestPlan(c)
	c.savedSettings.MaxCacheBytes++
	if _, err := c.applySetupPlan(p); err == nil {
		t.Fatal("stale review accepted")
	}
	p = setupTestPlan(c)
	c.importStatus.Running = true
	if _, err := c.applySetupPlan(p); err == nil {
		t.Fatal("running import would be interrupted")
	}
	c.importStatus.Running = false
	p.ImportSource = p.StorageDir
	if err := os.MkdirAll(p.StorageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.applySetupPlan(p); err == nil {
		t.Fatal("import source within target accepted")
	}
}

func TestSetupContinuationNeverReplaysAnInterruptedImport(t *testing.T) {
	for _, phase := range []string{"preparing", "importing"} {
		t.Run(phase, func(t *testing.T) {
			c := testConsole(t)
			version, _ := c.setupSettingsVersion(c.settings)
			a := SetupApplication{ID: "interrupted", Phase: phase, ConfigSaved: true, ImportSource: t.TempDir(), TargetVersion: version}
			if err := c.writeSetupApplication(a); err != nil {
				t.Fatal(err)
			}
			c.loadSetupApplication()
			c.resumeSetupApplication(func(string) error { t.Fatal("replayed interrupted operation"); return nil })
			if c.setupApplication.Phase != "failed" {
				t.Fatal(c.setupApplication)
			}
		})
	}
}

func TestSetupContinuationReportsPartialFailureAndConfigMismatch(t *testing.T) {
	c := testConsole(t)
	version, _ := c.setupSettingsVersion(c.settings)
	a := SetupApplication{ID: "partial", Phase: "restart", ConfigSaved: true, ImportSource: t.TempDir(), TargetVersion: version}
	if err := c.writeSetupApplication(a); err != nil {
		t.Fatal(err)
	}
	c.resumeSetupApplication(func(string) error { return errors.New("source disappeared") })
	if c.setupApplication.Phase != "failed" || !c.setupApplication.ConfigSaved || !strings.Contains(c.setupApplication.Error, "source disappeared") {
		t.Fatal(c.setupApplication)
	}
	a.ID, a.TargetVersion = "mismatch", "different"
	if err := c.writeSetupApplication(a); err != nil {
		t.Fatal(err)
	}
	c.resumeSetupApplication(func(string) error { t.Fatal("imported to unapproved configuration"); return nil })
	if !strings.Contains(c.setupApplication.Error, "不一致") {
		t.Fatal(c.setupApplication)
	}
}

func TestSetupApplyCredentialContinuationContainsNoPassword(t *testing.T) {
	c := testConsole(t)
	p := setupTestPlan(c)
	password := "fixture-private-password"
	p.UpstreamMode, p.SOCKS5Address, p.SOCKS5User, p.Password = "socks5", "127.0.0.1:1080", "fixture", &password
	a, err := c.applySetupPlan(p)
	if err != nil || a.Phase != "restart" {
		t.Fatalf("%+v %v", a, err)
	}
	data, _ := os.ReadFile(c.files().State("setup-apply"))
	if bytes.Contains(data, []byte(password)) || bytes.Contains(data, []byte("socks5Password")) {
		t.Fatal("continuation contains a secret")
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	loaded.resumeSetupApplication(func(string) error { t.Fatal("unexpected import"); return nil })
	if loaded.setupApplication.Phase != "completed" || loaded.settings.SOCKS5Password != password {
		t.Fatal("credential configuration did not survive restart", loaded.setupApplication)
	}
}

func TestSetupPlanEndpointsRequireConsentAndToken(t *testing.T) {
	c := testConsole(t)
	for _, path := range []string{"validate", "apply", "receipt"} {
		body, _ := json.Marshal(setupTestPlan(c))
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/"+path, bytes.NewReader(body))
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("missing token: %d", w.Code)
		}
		c.mu.Lock()
		terms := c.terms
		c.terms = termsReceipt{}
		c.mu.Unlock()
		r = httptest.NewRequest("POST", "http://"+c.address+"/api/setup/"+path, bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w = httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 428 {
			t.Fatalf("missing consent: %d", w.Code)
		}
		c.mu.Lock()
		c.terms = terms
		c.mu.Unlock()
	}
}

func TestSetupSettingsVersionIsKeyedStableAndCredentialSensitive(t *testing.T) {
	c := testConsole(t)
	s := c.savedSettings
	s.SOCKS5Password = "guessable-test-password"
	version, err := c.setupSettingsVersion(s)
	if err != nil || !strings.HasPrefix(version, "hmac-v1-") {
		t.Fatal("missing keyed version", err)
	}
	data, _ := json.Marshal(s)
	legacy := sha256.Sum256(data)
	if strings.Contains(version, hex.EncodeToString(legacy[:])) {
		t.Fatal("public version still validates unkeyed password guesses")
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if again, err := loaded.setupSettingsVersion(s); err != nil || again != version {
		t.Fatal("version changed across restart", err)
	}
	seen := map[string]bool{version: true}
	for _, change := range []func(*Settings){
		func(s *Settings) { s.SOCKS5Password += "-changed" },
		func(s *Settings) { s.MaxCacheBytes++ },
		func(s *Settings) { s.proxySecretError = "unreadable"; s.proxySecret = "protected-secret-a" },
		func(s *Settings) { s.proxySecretError = "unreadable"; s.proxySecret = "protected-secret-b" },
	} {
		changed := s
		change(&changed)
		got, err := c.setupSettingsVersion(changed)
		if err != nil || seen[got] {
			t.Fatal("version ignored changed configuration", err)
		}
		seen[got] = true
	}
	// A different secret key changes the verifier even for identical settings.
	before, _ := c.setupSettingsVersion(s)
	if err := os.WriteFile(c.files().ChannelKey, bytes.Repeat([]byte{42}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if after, err := c.setupSettingsVersion(s); err != nil || after == before {
		t.Fatal("version did not depend on key", err)
	}
}

func TestSetupVersionKeyFailureNeverFallsBackOrResumes(t *testing.T) {
	c := testConsole(t)
	p := setupTestPlan(c)
	if err := os.WriteFile(c.files().ChannelKey, []byte("corrupt-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if version, err := c.setupSettingsVersion(c.settings); err == nil || version != "" {
		t.Fatal("unsafe fallback version")
	}
	if _, err := c.applySetupPlan(p); err == nil {
		t.Fatal("applied without stable key")
	}
	c.setupApplication = SetupApplication{ID: "pending", Phase: "restart", TargetVersion: "", ConfigSaved: true, ImportSource: t.TempDir()}
	c.resumeSetupApplication(func(string) error { t.Fatal("resumed without key"); return nil })
	if c.setupApplication.Phase != "failed" {
		t.Fatal("missing key did not stop continuation")
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var state struct {
		Version string `json:"setupSettingsVersion"`
		Error   string `json:"setupSettingsError"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || state.Version != "" || state.Error == "" {
		t.Fatal("missing public failure state", err)
	}
	if data, _ := os.ReadFile(c.files().ChannelKey); string(data) != "corrupt-key" {
		t.Fatal("overwrote corrupt key")
	}
}

func TestSetupReceiptLookupPreservesOlderApplicationAndHidesLegacyVerifier(t *testing.T) {
	c := testConsole(t)
	first := SetupApplication{ID: "older", Phase: "completed", ConfigSaved: true, TargetVersion: strings.Repeat("a", 64)}
	if err := c.writeSetupApplication(first); err != nil {
		t.Fatal(err)
	}
	second := SetupApplication{ID: "newer", Phase: "completed", ConfigSaved: true}
	if err := c.writeSetupApplication(second); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(c.files().State("setup-apply"))
	for _, path := range []string{"receipt", "apply"} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/"+path, strings.NewReader(`{"id":"older"}`))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var got SetupApplication
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ID != first.ID || got.TargetVersion != "" {
			t.Fatal("old receipt not safely returned", w.Code)
		}
	}
	after, _ := os.ReadFile(c.files().State("setup-apply"))
	if !bytes.Equal(before, after) || c.setupApplication != second {
		t.Fatal("read lookup changed shared state")
	}
	select {
	case <-c.RestartRequested():
		t.Fatal("lookup restarted application")
	default:
	}
	c.setupApplication = first
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	if bytes.Contains(w.Body.Bytes(), []byte(first.TargetVersion)) || bytes.Contains(w.Body.Bytes(), []byte("targetVersion")) {
		t.Fatal("legacy credential verifier exposed")
	}
}

func TestSetupOldReceiptCannotReplayAfterAnotherApplication(t *testing.T) {
	c := testConsole(t)
	p := setupTestPlan(c)
	first, err := c.applySetupPlan(p)
	if err != nil || first.Phase != "completed" {
		t.Fatalf("first: %+v %v", first, err)
	}
	second := setupTestPlan(c)
	second.ID = "another-application"
	if _, err := c.applySetupPlan(second); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(c.files().State("setup-apply"))
	// Even a changed payload with an old id must only return the old receipt.
	p.ImportSource = "does-not-exist"
	if got, err := c.applySetupPlan(p); err != nil || got.ID != first.ID || got.Phase != "completed" {
		t.Fatalf("replay: %+v %v", got, err)
	}
	after, _ := os.ReadFile(c.files().State("setup-apply"))
	if !bytes.Equal(before, after) || c.service != nil {
		t.Fatal("old receipt caused a new application")
	}
}

func TestSetupChecksDoNotWaitForHostsLifecycleOperation(t *testing.T) {
	c := testConsole(t)
	p := setupTestPlan(c)
	p.Page = "storage"
	c.lifecycleMu.Lock() // An outstanding hosts/UAC operation owns this lock.
	done := make(chan error, 1)
	go func() {
		_, err := c.validateSetupPlan(p)
		if err == nil {
			_, err = c.inspectSetupStorage(p.StorageDir)
		}
		done <- err
	}()
	select {
	case err := <-done:
		c.lifecycleMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		c.lifecycleMu.Unlock()
		<-done
		t.Fatal("draft inspection/navigation waited for hosts")
	}
}
