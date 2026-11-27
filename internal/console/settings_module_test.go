package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"still-wanna-dance/internal/catalogmeta"
)

func TestDirectPresetIPsFromFile(t *testing.T) {
	const custom = `["192.0.2.7", "2001:0db8::7", "2001:db8::7"]`
	for _, mode := range []string{"direct", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data := `{"schemaVersion":8,"upstreamMode":"` + mode + `","socks5Address":"127.0.0.1:9","directPresetIPs":` + custom + `}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := New(path, "127.0.0.1:18081", testWorkDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			pool := c.requestChannel.Snapshot().Candidates
			if mode == "direct" {
				for _, target := range []string{catalogmeta.UdonURL, "http://api.udon.dance/Api/Songs/play?id=42", "http://play.udon.dance/video"} {
					var ips []string
					for _, candidate := range pool.Current(target) {
						ips = append(ips, candidate.IP)
					}
					if !slices.Equal(ips, []string{"2001:db8::7", "192.0.2.7"}) {
						t.Fatalf("%s candidates = %v", target, ips)
					}
				}
				if candidates := pool.Current(catalogmeta.UdonHTTPURL); len(candidates) != 0 {
					t.Fatal("presets leaked to the HTTP backup catalog", candidates)
				}
			} else if c.dns != nil || pool != nil {
				t.Fatal("proxy created a direct candidate pool")
			}
			if err := c.editor.saveTheme("dark"); err != nil {
				t.Fatal(err)
			}
			_, fields, err := readConfigFile(path)
			var retained []string
			if err != nil || json.Unmarshal(fields["directPresetIPs"], &retained) != nil || !slices.Equal(retained, []string{"192.0.2.7", "2001:0db8::7", "2001:db8::7"}) {
				t.Fatalf("settings save lost file override: %s (%v)", fields["directPresetIPs"], err)
			}
		})
	}
}

func TestDirectPresetIPsFileDefaultsAndErrors(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", `["::1"]`, `["10.0.0.1"]`, `["fd00::1"]`, `["ff02::1"]`, `["0.0.0.0"]`, `["example.org"]`, `["1.1.1.1:80"]`, `"bad"`} {
		path := filepath.Join(t.TempDir(), "config.json")
		data := `{"schemaVersion":8`
		if raw != "" {
			data += `,"directPresetIPs":` + raw
		}
		data += "}"
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadConfigFile(path)
		if raw != "" && raw != "null" && raw != "[]" {
			if err == nil || !strings.Contains(err.Error(), "directPresetIPs") {
				t.Fatalf("%s: expected a field error, got %v", raw, err)
			}
			continue
		}
		if err != nil || loaded.Settings.DirectPresetIPs != "" {
			t.Fatalf("%s: expected built-in defaults: %v", raw, err)
		}
		if err := saveConfigSettings(path, loaded.Settings); err != nil {
			t.Fatal(err)
		}
		_, fields, err := readConfigFile(path)
		if err != nil || fields["directPresetIPs"] != nil {
			t.Fatalf("%s: saved a default list: %v", raw, err)
		}
	}
}

func TestDirectPresetIPsSettingsLifecycle(t *testing.T) {
	c := testConsole(t)
	const custom = `["192.0.2.7","2001:db8::7"]`
	save := func(patch string) {
		t.Helper()
		if err := c.editor.saveSettingsPatch([]byte(patch)); err != nil {
			t.Fatal(err)
		}
	}
	save(`{"directPresetIPs":` + custom + `}`)
	save(`{"directPresetIPs":null,"downloadGrowIntervalMS":400}`)
	save(`{"upstreamMode":"proxy","socks5Address":"127.0.0.1:9"}`)
	if c.settings.DirectPresetIPs != "" || c.editor.savedSettings.DirectPresetIPs != custom {
		t.Fatal("active or saved override changed unexpectedly")
	}
	c = restartTestConsole(t, c)
	if c.settings.DirectPresetIPs != custom || c.settings.UpstreamMode != "proxy" {
		t.Fatal("proxy restart lost the override")
	}
	save(`{"upstreamMode":"direct"}`)
	c = restartTestConsole(t, c)
	if c.settings.DirectPresetIPs != custom || c.settings != c.editor.savedSettings {
		t.Fatal("direct restart lost the override or still requires restart")
	}
	save(`{"directPresetIPs":[]}`)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields["directPresetIPs"] != nil || c.settings.DirectPresetIPs != custom {
		t.Fatal("clear did not remove the on-disk override independently of the active list", err)
	}
}

func TestDirectPresetIPsHTTPBodyLimit(t *testing.T) {
	ips := make([]string, 1000)
	for i := range ips {
		ips[i] = fmt.Sprintf("2001:0db8:0001:0002:0003:0004:0005:%04x", i)
	}
	body, err := json.Marshal(map[string][]string{"directPresetIPs": ips})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"normal", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			var handler http.Handler
			var host, token, config string
			if mode == "normal" {
				c := testConsole(t)
				handler, host, token, config = c, c.address, c.token, c.configPath
			} else {
				r := recoveryFixture(t, `{"schemaVersion":8,"directPresetIPs":"wrong type"}`)
				handler, host, token, config = r, r.address, r.token, r.identity.Config
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			post := func(path, payload string, want int, wantError string) {
				t.Helper()
				req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.Host = host
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-SWD-Token", token)
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				reply, err := io.ReadAll(res.Body)
				if err != nil || res.StatusCode != want {
					t.Fatalf("%s (%d bytes): HTTP %d, %s (%v)", path, len(payload), res.StatusCode, reply, err)
				}
				if wantError != "" && !bytes.Contains(reply, []byte(wantError)) {
					t.Fatalf("%s: unexpected rejection: %s", path, reply)
				}
			}
			const limit = 64 << 10
			const tooLarge = "request body too large"
			post("/api/settings", string(body)+strings.Repeat(" ", limit-len(body)), http.StatusOK, "")
			const clearList = `{"directPresetIPs":[]}`
			padding := strings.Repeat(" ", limit+1-len(clearList))
			post("/api/settings", padding+clearList, http.StatusBadRequest, tooLarge)
			post("/api/settings", clearList+padding, http.StatusBadRequest, tooLarge)
			post("/api/settings", clearList+"\n{}", http.StatusBadRequest, "after top-level value")
			const theme = `{"theme":"dark"}`
			post("/api/settings/theme", strings.Repeat(" ", (8<<10)+1-len(theme))+theme, http.StatusBadRequest, tooLarge)
			_, fields, err := readConfigFile(config)
			if err != nil {
				t.Fatal(err)
			}
			var saved []string
			if err := json.Unmarshal(fields["directPresetIPs"], &saved); err != nil || !slices.Equal(saved, ips) {
				t.Fatalf("saved IP list differs from successful request (%d entries, %v)", len(saved), err)
			}
		})
	}
}

func TestConfigLoadDoesNotPrepareResourcesOrRewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	loaded, err := loadConfigFile(path)
	if err != nil || loaded.Exists || loaded.Settings.PrepareAheadSeconds != 500 {
		t.Fatal("missing file did not return normalized defaults", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("loading defaults created files", err)
	}
	data := []byte(`{"schemaVersion":8,"databasePath":"db/library.sqlite","videoDir":"videos","vrchatLogDir":"logs","autoOpenWebUI":false,"downloadGrowIntervalMS":0,"localProtectionGraceSeconds":null,"vrcxImportDismissed":true,"futureValue":9007199254740993}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := loaded.Settings
	if !loaded.Exists || !loaded.VRCXImportDismissed || s.AutoOpenWebUI || s.DownloadGrowIntervalMS != 0 || s.LocalProtectionGraceSeconds != 300 {
		t.Fatal("load lost explicit values or defaults")
	}
	if s.DatabasePath != filepath.Join(root, "db", "library.sqlite") || s.VideoDir != filepath.Join(root, "videos") || s.VRChatLogDir != filepath.Join(root, "logs") {
		t.Fatal("paths did not resolve relative to the configuration")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatal("load prepared resources", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) {
		t.Fatal("load rewrote the file", err)
	}
}

func TestSettingsRequestOmissionSemantics(t *testing.T) {
	for _, variant := range []string{"missing", "null", "explicit"} {
		t.Run(variant, func(t *testing.T) {
			c := testConsole(t)
			if err := c.editor.saveTheme("dark"); err != nil {
				t.Fatal(err)
			}
			saved := c.editor.savedSettings
			saved.DownloadConcurrencySoftLimit, saved.DownloadGrowIntervalMS = 23, 600
			if err := c.editor.save(saved); err != nil {
				t.Fatal(err)
			}
			active := c.settings
			fields := map[string]any{
				"requestRetentionMode": "unlimited", "videoStorageMode": "none",
				"databasePath": active.DatabasePath, "videoDir": active.VideoDir,
			}
			if variant != "missing" {
				for _, key := range []string{"downloadConcurrencySoftLimit", "downloadGrowIntervalMS", "localProtectionGraceSeconds", "restoreHostsOnStopOrExit", "retainFavorites", "theme", "autoOpenWebUI", "queueWithGateway"} {
					fields[key] = nil
				}
			}
			if variant == "explicit" {
				fields["downloadConcurrencySoftLimit"] = 32
				fields["downloadGrowIntervalMS"], fields["localProtectionGraceSeconds"] = 0, 0
				fields["restoreHostsOnStopOrExit"], fields["retainFavorites"] = false, false
				fields["autoOpenWebUI"], fields["queueWithGateway"] = true, true
				fields["vrchatLogDir"] = ""
			}
			body, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(body))
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			want := saved
			if variant == "explicit" {
				want.DownloadConcurrencySoftLimit = 32
				want.DownloadGrowIntervalMS, want.LocalProtectionGraceSeconds = 0, 0
				want.RestoreHostsOnStopOrExit, want.RetainFavorites = false, false
				want.AutoOpenWebUI = true
				want.VRChatLogDir = "" // explicitly clearing the path selects automatic detection
			}
			active.RestoreHostsOnStopOrExit = want.RestoreHostsOnStopOrExit
			if c.editor.savedSettings != want || c.settings != active {
				t.Fatal("partial request lost saved settings or applied staged values")
			}
			before, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(`{"videoStorageMode":""}`))
			r.Header.Set("X-SWD-Token", c.token)
			w = httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal("invalid partial settings accepted", w.Code)
			}
			if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(after, before) || c.editor.savedSettings != want {
				t.Fatal("invalid request changed settings", err)
			}
		})
	}
}

func TestConcurrentConfigurationUpdatesPreserveIndependentChoices(t *testing.T) {
	c := testConsole(t)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	fields["futureValue"] = json.RawMessage(`9007199254740993`)
	// Unreadable protected data must also survive unrelated updates unchanged.
	fields["proxySecret"] = json.RawMessage(`{"version":999,"ciphertext":"unavailable"}`)
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	active, next := c.settings, c.editor.savedSettings
	next.PrepareAheadSeconds++
	patch, err := json.Marshal(map[string]int{"prepareAheadSeconds": next.PrepareAheadSeconds})
	if err != nil {
		t.Fatal(err)
	}
	next.LogLevel = "warn"
	start, results := make(chan struct{}), make(chan error, 4)
	for _, update := range []func() error{
		func() error { return c.editor.saveSettingsPatch(patch) },
		func() error { return c.editor.saveSettingsPatch([]byte(`{"logLevel":"warn"}`)) },
		func() error { return c.editor.saveTheme("dark") },
		c.dismissVRCXSuggestion,
	} {
		go func() { <-start; results <- update() }()
	}
	close(start)
	for range 4 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	next.Theme, active.Theme = "dark", "dark"
	if c.editor.savedSettings != next || c.settings != active || !c.vrcxImportDismissed {
		t.Fatal("concurrent writes lost a choice or applied staged settings")
	}
	_, after, err := readConfigFile(c.configPath)
	if err != nil || string(after["futureValue"]) != "9007199254740993" {
		t.Fatal("unknown field lost precision", err)
	}
	var secret bytes.Buffer
	if err := json.Compact(&secret, after["proxySecret"]); err != nil || !bytes.Equal(secret.Bytes(), fields["proxySecret"]) {
		t.Fatal("unrelated updates replaced unreadable credentials", err)
	}
	c = restartTestConsole(t, c)
	if c.settings != next || !c.vrcxImportDismissed {
		t.Fatal("reopening lost concurrent updates")
	}
}

func TestVRCXDismissalRejectsMissingAndFutureConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfigVRCXDismissal(path); !os.IsNotExist(err) {
		t.Fatal("dismissal recreated a missing configuration", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing configuration was written", err)
	}
	data := []byte(`{"schemaVersion":999,"futureValue":9007199254740993}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigVRCXDismissal(path); err == nil {
		t.Fatal("dismissal accepted a future version")
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) {
		t.Fatal("dismissal changed a future configuration", err)
	}
}
