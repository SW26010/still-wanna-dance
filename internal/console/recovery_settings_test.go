package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func recoveryFixture(t *testing.T, contents string) *Recovery {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := NewRecovery("127.0.0.1:12345", desktop.Identity{Config: config, Executable: filepath.Join(root, "SWD.exe")}, "startup failed")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { view.BeginShutdown(); view.Close(context.Background()) })
	return view
}

func recoveryRequest(view *Recovery, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+view.console.address+path, strings.NewReader(body))
	req.Header.Set("X-SWD-Token", view.console.token)
	w := httptest.NewRecorder()
	view.ServeHTTP(w, req)
	return w
}

func TestRecoveryRepairsConfigurationPreservingUnrelatedFields(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,"videoStorageMode":"limited","videoBudgetBytes":0,"future":{"keep":true},"proxySecret":{"unrecognized":"preserve"},"proxyNodes":{"unrecognized":"preserve"}}`)
	w := recoveryRequest(view, "GET", "/api/recovery/settings", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"videoBudgetBytes":0`) || !strings.Contains(w.Body.String(), `"fields"`) || strings.Contains(w.Body.String(), "unrecognized") {
		t.Fatal(w.Code, w.Body.String())
	}
	if lease, err := desktop.LockConfig(view.identity.Config); err == nil {
		lease.Close()
		t.Fatal("recovery did not retain exclusive config lease")
	}
	before, _ := os.ReadFile(view.identity.Config)
	w = recoveryRequest(view, "POST", "/api/settings", `{"videoBudgetBytes":0}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"settings_validation"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(view.identity.Config)
	if string(before) != string(after) {
		t.Fatal("invalid save changed config")
	}
	w = recoveryRequest(view, "POST", "/api/settings", `{"videoBudgetBytes":1073741824}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, fields, err := readConfigFile(view.identity.Config)
	if err != nil || !strings.Contains(string(fields["future"]), `"keep": true`) || !strings.Contains(string(fields["proxySecret"]), "preserve") || !strings.Contains(string(fields["proxyNodes"]), "preserve") {
		t.Fatal(fields, err)
	}
	if _, err := ReadStartupSettings(view.identity.Config); err != nil {
		t.Fatal(err)
	}
	w = recoveryRequest(view, "POST", "/api/restart", `{}`)
	if w.Code != 200 || view.ShutdownAction() != ShutdownRestart {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = recoveryRequest(view, "POST", "/api/settings", `{"videoBudgetBytes":2147483648}`); w.Code != 503 {
		t.Fatal("save after restart", w.Code)
	}
	view.Close(context.Background())
	lease, err := desktop.LockConfig(view.identity.Config)
	if err != nil {
		t.Fatal("lease not released", err)
	}
	lease.Close()
}

func TestRecoveryRejectsUnreadableOrUnsupportedDocuments(t *testing.T) {
	for _, content := range []string{`{"schemaVersion":8,`, `{"schemaVersion":99}`, `{"schemaVersion":8,"videoBudgetBytes":"wrong type"}`} {
		t.Run(content, func(t *testing.T) {
			view := recoveryFixture(t, content)
			w := recoveryRequest(view, "GET", "/api/recovery/settings", "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"unavailable"`) || strings.Contains(w.Body.String(), `"settings"`) {
				t.Fatal(w.Code, w.Body.String())
			}
			if w = recoveryRequest(view, "POST", "/api/settings", `{"videoBudgetBytes":1}`); w.Code != 503 {
				t.Fatal(w.Code, w.Body.String())
			}
			data, _ := os.ReadFile(view.identity.Config)
			if string(data) != content {
				t.Fatal("source changed")
			}
		})
	}
}

func TestRecoverySharesCrossFieldValidationAndMergesLatest(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8}`)
	for _, patch := range []string{`{"requestRetentionMode":"limited","requestRetentionDays":0}`, `{"videoStorageMode":"limited","videoBudgetBytes":0}`, `{"upstreamMode":"proxy","socks5Address":""}`, `{"downloadConcurrencySoftLimit":65}`} {
		w := recoveryRequest(view, "POST", "/api/settings", patch)
		if w.Code != 400 {
			t.Fatal(patch, w.Code, w.Body.String())
		}
	}
	for _, patch := range []string{`{"downloadConcurrencySoftLimit":8}`, `{"downloadGrowIntervalMS":100}`} {
		if w := recoveryRequest(view, "POST", "/api/settings", patch); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s, err := ReadStartupSettings(view.identity.Config)
	if err != nil || s.DownloadConcurrencySoftLimit != 8 || s.DownloadGrowIntervalMS != 100 {
		t.Fatal(s, err)
	}
	// A manual edit after the page opened is merged; malformed edits are refused.
	if err := updateConfigFile(view.identity.Config, func(fields map[string]json.RawMessage, _ bool) error {
		fields["logLevel"] = json.RawMessage(`"debug"`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := recoveryRequest(view, "POST", "/api/settings", `{"autoAccelerate":false}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _ = ReadStartupSettings(view.identity.Config)
	if s.LogLevel != "debug" {
		t.Fatal("stale save overwrote unrelated setting")
	}
	os.WriteFile(view.identity.Config, []byte(`{`), 0600)
	if w := recoveryRequest(view, "POST", "/api/settings", `{"autoAccelerate":true}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := recoveryRequest(view, "POST", "/api/restart", `{}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRecoverySettingsRejectsUnauthorizedAndWriteFailure(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8}`)
	req := httptest.NewRequest("GET", "http://"+view.console.address+"/api/recovery/settings", nil)
	w := httptest.NewRecorder()
	view.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	// A blocked atomic-write slot must not publish a successful snapshot.
	slot := filepath.Join(filepath.Dir(view.identity.Config), ".config.json.swd-tmp")
	if err := os.Mkdir(slot, 0700); err != nil {
		t.Fatal(err)
	}
	w = recoveryRequest(view, "POST", "/api/settings", `{"downloadConcurrencySoftLimit":8}`)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _ := ReadStartupSettings(view.identity.Config)
	if s.DownloadConcurrencySoftLimit == 8 {
		t.Fatal("failed write changed config")
	}
}

func TestRecoveryStorageConfirmationAndResourceBoundaries(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8}`)
	video := t.TempDir()
	marker := filepath.Join(video, strings.Repeat("a", 32)+".mp4")
	if err := os.WriteFile(marker, []byte("preserve video"), 0600); err != nil {
		t.Fatal(err)
	}
	post := func(body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		return recoveryRequest(view, "POST", "/api/settings", string(data))
	}
	before, _ := os.ReadFile(view.identity.Config)
	w := post(map[string]any{"videoDir": video})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"fields"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(view.identity.Config)
	if string(before) != string(after) {
		t.Fatal("saved before video policy confirmation")
	}
	w = post(map[string]any{"videoDir": video, "confirmVideoPolicy": videoPolicyConfirmation{VideoDir: video, VideoStorageMode: "unlimited", RetainFavorites: true}})
	if w.Code != 400 {
		t.Fatal("accepted mismatched confirmation", w.Code, w.Body.String())
	}
	w = post(map[string]any{"videoDir": video, "confirmVideoPolicy": videoPolicyConfirmation{VideoDir: video, VideoStorageMode: "none", RetainFavorites: true}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if data, _ := os.ReadFile(marker); string(data) != "preserve video" {
		t.Fatal("recovery touched video")
	}
	missing := filepath.Join(t.TempDir(), "uncreated", "storage.sqlite")
	w = post(map[string]any{"databasePath": missing})
	if w.Code != 400 {
		t.Fatal("recovery accepted uncreated database", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatal("recovery created resource directories", err)
	}
	w = post(map[string]any{"databasePath": view.identity.Config})
	if w.Code != 400 {
		t.Fatal("recovery allowed config collision", w.Code, w.Body.String())
	}
}

func TestRecoveryPreservesDecodedPasswordWithoutExposingIt(t *testing.T) {
	secret, err := storedProxySecret(Settings{SOCKS5Password: "fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"schemaVersion": 8, "proxySecret": secret, "upstreamMode": "proxy", "socks5Address": "127.0.0.1:7890", "socks5Username": "fixture-user", "downloadConcurrencySoftLimit": 65})
	view := recoveryFixture(t, string(data))
	w := recoveryRequest(view, "GET", "/api/recovery/settings", "")
	if strings.Contains(w.Body.String(), "fixture-password") || strings.Contains(w.Body.String(), `"socks5Password"`) {
		t.Fatal("password leaked")
	}
	w = recoveryRequest(view, "POST", "/api/settings", `{"downloadConcurrencySoftLimit":8}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, err := ReadStartupSettings(view.identity.Config)
	if err != nil || s.SOCKS5Password != "fixture-password" {
		t.Fatal("password not preserved", err)
	}
}

func TestRecoveryDoesNotStealConfigurationLease(t *testing.T) {
	owner := recoveryFixture(t, `{"schemaVersion":8}`)
	view, err := NewRecovery("127.0.0.1:12346", owner.identity, "config locked")
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close(context.Background())
	w := recoveryRequest(view, "GET", "/api/recovery/settings", "")
	if !strings.Contains(w.Body.String(), `"unavailable"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = recoveryRequest(view, "POST", "/api/settings", `{"logLevel":"debug"}`)
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRecoveryAcknowledgesWinningShutdown(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,"videoStorageMode":"limited","videoBudgetBytes":0}`)
	if w := recoveryRequest(view, "POST", "/api/exit", `{}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	// Even invalid settings must not change or obscure an accepted exit.
	w := recoveryRequest(view, "POST", "/api/restart", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"action":"exit"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRecoveryResolvesDefaultSyntaxWhileRetainingInvalidValues(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,"logLevel":"LOUD","theme":"","upstreamMode":"","proxyBackend":"","prepareAheadSeconds":0,"downloadGrowIntervalMS":0,"localProtectionGraceSeconds":0}`)
	s := view.console.savedSettings
	defaults, _ := normalizeSettings(defaultSettings())
	if s.LogLevel != "LOUD" || s.Theme != "system" || s.UpstreamMode != "direct" || s.ProxyBackend != "external-socks5" || s.PrepareAheadSeconds != defaults.PrepareAheadSeconds || s.DownloadGrowIntervalMS != 0 || s.LocalProtectionGraceSeconds != 0 {
		t.Fatal("default or explicit zero semantics changed", s)
	}
}

func TestRecoveryUsesNormalDatabaseAndProxyOperations(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,"videoStorageMode":"limited","videoBudgetBytes":0}`)
	paths := map[string]string{"databasePath": filepath.Join(t.TempDir(), "new.sqlite"), "videoDir": t.TempDir()}
	body, _ := json.Marshal(paths)
	w := recoveryRequest(view, "POST", "/api/storage/prepare-database", string(body))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"created":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Database creation and import remain usable while unrelated settings are invalid.
	w = recoveryRequest(view, "POST", "/api/proxy/import", nodeFixture)
	var report proxyImportResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil || report.ID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	patch := map[string]any{"databasePath": paths["databasePath"], "videoDir": paths["videoDir"], "videoBudgetBytes": 1073741824, "upstreamMode": "proxy", "proxyBackend": "mihomo", "proxyImportID": report.ID}
	body, _ = json.Marshal(patch)
	w = recoveryRequest(view, "POST", "/api/settings", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, err := ReadStartupSettings(view.identity.Config)
	if err != nil || s.nodeSnapshot != nodeFixture || s.DatabasePath != paths["databasePath"] {
		t.Fatal("shared save lost resources", err)
	}
	if view.console.store != nil || view.console.client != nil || view.console.terms.valid() {
		t.Fatal("settings enabled business or consent")
	}
	if w := recoveryRequest(view, "POST", "/api/activation/enable", `{}`); w.Code != 503 {
		t.Fatal("business enabled", w.Code)
	}
}

func TestRecoveryRestorePolicyUsesNormalAuthorizationAndCleanup(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,"restoreHostsOnStopOrExit":false}`)
	managed, err := transformHosts(hostsBaseline, "enable")
	if err != nil {
		t.Fatal(err)
	}
	path := hostsFixture(t, managed)
	view.console.hosts.close(false)
	view.console.hosts = newHostsController(path, func(context.Context) (hostsConnection, error) { return nil, errHostsAuthorizationCanceled })
	w := recoveryRequest(view, "POST", "/api/settings", `{"restoreHostsOnStopOrExit":true}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"uac_canceled"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _ := ReadStartupSettings(view.identity.Config)
	if s.RestoreHostsOnStopOrExit {
		t.Fatal("canceled authorization was saved")
	}
	view.console.hosts.close(false)
	h, launches := fixtureHostsController(t, managed)
	view.console.hosts = h
	w = recoveryRequest(view, "POST", "/api/settings", `{"restoreHostsOnStopOrExit":true}`)
	if w.Code != 200 || *launches != 1 {
		t.Fatal(w.Code, w.Body.String(), *launches)
	}
	if readHostsFixture(t, h.path) != managed {
		t.Fatal("permission preparation changed hosts")
	}
	if err := view.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hasManagedHosts(readHostsFixture(t, h.path)) {
		t.Fatal("committed exit policy not applied")
	}
}

func TestRecoveryUnavailableDisablesEverySettingsAction(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8,`)
	for _, path := range []string{"/api/settings", "/api/settings/theme", "/api/path/select", "/api/storage/prepare-database", "/api/storage/check", "/api/proxy/import", "/api/proxy/import/discard"} {
		w := recoveryRequest(view, "POST", path, `{}`)
		if w.Code != 503 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}

func TestRecoveryInvalidThemeUsesSystemAppearance(t *testing.T) {
	for _, contents := range []string{`{"schemaVersion":8,"theme":"invalid"}`, `{"schemaVersion":8,`} {
		view := recoveryFixture(t, contents)
		if state := view.DesktopState(); state != (desktop.State{Unavailable: true, Theme: "system"}) {
			t.Fatal("invalid configuration advertised an active service or theme", state)
		}
		if w := recoveryRequest(view, "GET", "/", ""); !strings.Contains(w.Body.String(), `"theme":"system"`) {
			t.Fatal("recovery page did not use the system appearance")
		}
	}
}

func TestRecoveryShutdownCancelsPendingPathSelection(t *testing.T) {
	view := recoveryFixture(t, `{"schemaVersion":8}`)
	started := make(chan struct{})
	view.console.pathPicker = func(ctx context.Context, _ desktop.PathDialog) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- recoveryRequest(view, "POST", "/api/path/select", `{"purpose":"database"}`) }()
	<-started
	if err := view.Close(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if w := <-done; w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Exercise the actual tray ordering and WebUI route: request exit first, then
// let the application owner cancel and drain. Calling Close directly misses a
// deadlock when RequestExit itself waits for the configuration transaction.
func TestRecoveryExitCancelsPendingSettingsOperation(t *testing.T) {
	for _, operation := range []string{"url-import", "authorization"} {
		for _, exit := range []string{"tray", "webui"} {
			t.Run(operation+"/"+exit, func(t *testing.T) {
				view := recoveryFixture(t, `{"schemaVersion":8,"restoreHostsOnStopOrExit":false}`)
				started := make(chan struct{})
				var run func() *httptest.ResponseRecorder
				if operation == "url-import" {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						close(started)
						<-r.Context().Done()
					}))
					defer upstream.Close()
					run = func() *httptest.ResponseRecorder {
						req := httptest.NewRequest("POST", "http://"+view.console.address+"/api/proxy/import", strings.NewReader(upstream.URL))
						req.Header.Set("X-SWD-Token", view.console.token)
						req.Header.Set("X-SWD-Import-Source", "url")
						w := httptest.NewRecorder()
						view.ServeHTTP(w, req)
						return w
					}
				} else {
					managed, err := transformHosts(hostsBaseline, "enable")
					if err != nil {
						t.Fatal(err)
					}
					view.console.hosts.close(false)
					view.console.hosts = newHostsController(hostsFixture(t, managed), func(ctx context.Context) (hostsConnection, error) {
						close(started)
						<-ctx.Done()
						return nil, ctx.Err()
					})
					run = func() *httptest.ResponseRecorder {
						return recoveryRequest(view, "POST", "/api/settings", `{"restoreHostsOnStopOrExit":true}`)
					}
				}
				defer view.BeginShutdown()
				before, _ := os.ReadFile(view.identity.Config)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- run() }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("operation did not start")
				}
				exited := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					if exit == "tray" {
						view.RequestExit()
						exited <- nil
					} else {
						exited <- recoveryRequest(view, "POST", "/api/exit", `{}`)
					}
				}()
				select {
				case w := <-exited:
					if w != nil && w.Code != 200 {
						t.Fatal(w.Code, w.Body.String())
					}
				case <-time.After(2 * time.Second):
					t.Fatal("exit waited for the pending settings operation")
				}
				select {
				case <-view.ShutdownRequested():
				default:
					t.Fatal("application owner was not notified")
				}
				view.BeginShutdown()
				select {
				case w := <-done:
					if w.Code != 400 {
						t.Fatal(w.Code, w.Body.String())
					}
				case <-time.After(2 * time.Second):
					t.Fatal("shutdown did not cancel the operation")
				}
				after, _ := os.ReadFile(view.identity.Config)
				if string(before) != string(after) {
					t.Fatal("canceled operation changed configuration")
				}
				if err := view.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				lease, err := desktop.LockConfig(view.identity.Config)
				if err != nil {
					t.Fatal("shutdown retained the configuration lease", err)
				}
				lease.Close()
			})
		}
	}
}
