package console

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestVRCXBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_VRCX_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_VRCX_BROWSER_EVIDENCE for isolated real Console browser integration")
	}
	c := testConsole(t)
	source := vrcxFixture(t)
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	defaultSource := filepath.Join(appData, "VRCX", "VRCX.sqlite3")
	if err := os.MkdirAll(filepath.Dir(defaultSource), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultSource, data, 0600); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "vrcx-live.mjs"), ui.URL, source, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if count, err := cacheproxy.ReadSongUsageCount(context.Background(), c.settings.StorageDir); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if c.service != nil {
		t.Fatal("browser import started engine")
	}
}

func vrcxFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "VRCX.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE gamelog_video_play(created_at TEXT,video_url TEXT,video_id TEXT,location TEXT);
		INSERT INTO gamelog_video_play VALUES
		('2026-01-01T00:00:00.000Z','https://api.udon.dance/Api/Songs/play?id=1343&node=cf','wrong-id','private-location'),
		('2026-01-01T00:00:01.000Z','http://api.udon.dance/Api/Songs/play?node=nya&id=1343','wrong-id','private-location'),
		('2026-01-01T00:01:00.000Z','https://api.udon.dance/Api/Songs/play?id=1344','wrong-id','private-location'),
		('2026-01-01T00:01:00.000Z','https://private.invalid/secret','1345','private-location')`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func vrcxRequest(t *testing.T, c *Console, action string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/vrcx/"+action, strings.NewReader(string(data)))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func previewVRCX(t *testing.T, c *Console, path, root string) vrcxPreview {
	t.Helper()
	w := vrcxRequest(t, c, "preview", map[string]string{"path": path, "storageDir": root})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "video_url") || strings.Contains(w.Body.String(), "Api/Songs") {
		t.Fatal("private history escaped")
	}
	var p vrcxPreview
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVRCXPreviewApplyReplacementAndRetry(t *testing.T) {
	c := testConsole(t)
	source := vrcxFixture(t)
	beforeConfig, _ := os.ReadFile(c.configPath)
	p := previewVRCX(t, c, source, "")
	if p.ID == "" || p.Events != 2 || p.Duplicates != 1 || p.Skipped != 2 || p.Songs != 2 || p.ExistingSongs != 0 {
		t.Fatal(p)
	}
	if c.service != nil {
		t.Fatal("preview started engine")
	}
	if _, err := os.Stat(c.settings.StorageDir); !os.IsNotExist(err) {
		t.Fatal("preview created target")
	}
	w := vrcxRequest(t, c, "apply", map[string]any{"id": p.ID, "overwrite": false})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if c.service != nil {
		t.Fatal("idle import started engine")
	}
	if count, err := cacheproxy.ReadSongUsageCount(context.Background(), c.settings.StorageDir); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	// Same-token retries acknowledge completion; a later demand is not removed.
	db, err := sql.Open("sqlite", filepath.Join(c.settings.StorageDir, "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("INSERT INTO songs(song_id) VALUES (99); INSERT INTO song_usage VALUES (99,1,1,1000)"); err != nil {
		t.Fatal(err)
	}
	w = vrcxRequest(t, c, "apply", map[string]any{"id": p.ID})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if count, err := cacheproxy.ReadSongUsageCount(context.Background(), c.settings.StorageDir); err != nil || count != 3 {
		t.Fatal("retry replaced data", count, err)
	}
	p = previewVRCX(t, c, source, "")
	if p.ExistingSongs != 3 {
		t.Fatal(p)
	}
	w = vrcxRequest(t, c, "apply", map[string]any{"id": p.ID})
	if w.Code != 409 {
		t.Fatal("unconfirmed overwrite", w.Code)
	}
	w = vrcxRequest(t, c, "apply", map[string]any{"id": p.ID, "overwrite": true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=99").Scan(&count); err != nil || count != 0 {
		t.Fatal("unmatched old row survived", count, err)
	}
	afterConfig, _ := os.ReadFile(c.configPath)
	if string(beforeConfig) != string(afterConfig) {
		t.Fatal("import changed settings")
	}
	for _, table := range []string{"song_media", "song_urls", "request_events"} {
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("unexpected side effects", table, count, err)
		}
	}
}

func TestVRCXNewDemandAfterPreviewRequiresNewAuthorization(t *testing.T) {
	c := testConsole(t)
	source := vrcxFixture(t)
	p := previewVRCX(t, c, source, "")
	h := &cacheproxy.SongUsageImport{}
	h.Observe(99, time.Now().UnixMilli())
	if err := cacheproxy.ImportSongUsage(context.Background(), c.settings.StorageDir, h, false); err != nil {
		t.Fatal(err)
	}
	for _, overwrite := range []bool{false, true} {
		w := vrcxRequest(t, c, "apply", map[string]any{"id": p.ID, "overwrite": overwrite})
		if w.Code != 409 {
			t.Fatal("new demand lost without new preview", w.Code, w.Body.String())
		}
	}
	if count, err := cacheproxy.ReadSongUsageCount(context.Background(), c.settings.StorageDir); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestVRCXRejectsLostStorageWithRestoredUsageEvidence(t *testing.T) {
	source := vrcxFixture(t)
	for _, evidence := range []struct {
		kind   string
		result any
	}{
		{"inventory", Inventory{CoverageKnown: true}},
		{"batch", Batch{Total: 1}},
		{"attempt", Batch{Finished: time.Now()}},
	} {
		for _, stage := range []string{"preview", "apply"} {
			for _, layout := range []string{"missing-root", "empty-root"} {
				t.Run(evidence.kind+"/"+stage+"/"+layout, func(t *testing.T) {
					c := testConsole(t)
					if err := c.save(c.settings); err != nil {
						t.Fatal(err)
					}
					root := c.settings.StorageDir
					history := &cacheproxy.SongUsageImport{}
					history.Observe(99, time.Now().UnixMilli())
					if err := cacheproxy.ImportSongUsage(t.Context(), root, history, false); err != nil {
						t.Fatal(err)
					}
					if err := c.writeSnapshot(evidence.kind, root, evidence.result); err != nil {
						t.Fatal(err)
					}
					c = restartTestConsole(t, c)
					if c.service != nil {
						t.Fatal("test requires restored evidence without an engine")
					}
					snapshot, err := os.ReadFile(c.files().State(evidence.kind))
					if err != nil {
						t.Fatal(err)
					}
					database, err := os.ReadFile(filepath.Join(root, "storage.sqlite"))
					if err != nil {
						t.Fatal(err)
					}
					request := map[string]any{"path": source}
					if stage == "apply" {
						p := previewVRCX(t, c, source, "")
						if p.ExistingSongs != 1 {
							t.Fatal(p)
						}
						request = map[string]any{"id": p.ID, "overwrite": true}
					}
					moved := root + "-unavailable"
					if err := os.Rename(root, moved); err != nil {
						t.Fatal(err)
					}
					if layout == "empty-root" {
						if err := os.Mkdir(root, 0700); err != nil {
							t.Fatal(err)
						}
					}
					w := vrcxRequest(t, c, stage, request)
					if w.Code != 409 {
						t.Fatalf("lost storage accepted: %d %s", w.Code, w.Body.String())
					}
					if c.service != nil || (c.vrcxPreview != nil && c.vrcxPreview.Imported) {
						t.Fatal("rejection started an engine or completed the import")
					}
					if layout == "empty-root" {
						if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
							t.Fatal("rejection wrote to empty root", entries, err)
						}
					} else if _, err := os.Stat(root); !os.IsNotExist(err) {
						t.Fatal("rejection recreated root", err)
					}
					if got, err := os.ReadFile(c.files().State(evidence.kind)); err != nil || !bytes.Equal(got, snapshot) {
						t.Fatal("rejection changed usage evidence", err)
					}
					if got, err := os.ReadFile(filepath.Join(moved, "storage.sqlite")); err != nil || !bytes.Equal(got, database) {
						t.Fatal("rejection changed original database", err)
					}
				})
			}
		}
	}
}

func TestVRCXDetectionReportsTargetUsageWithoutReadingHistory(t *testing.T) {
	c := testConsole(t)
	t.Setenv("APPDATA", t.TempDir())
	check := func(root string, want *int) {
		t.Helper()
		w := vrcxRequest(t, c, "detect", map[string]string{"storageDir": root})
		var result struct {
			ExistingSongs *int   `json:"existingSongs"`
			TargetError   string `json:"targetError"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if want == nil {
			if result.ExistingSongs != nil || result.TargetError == "" {
				t.Fatal("unavailable storage treated as empty", w.Body.String())
			}
		} else if result.ExistingSongs == nil || *result.ExistingSongs != *want || result.TargetError != "" {
			t.Fatal(w.Body.String())
		}
		if c.vrcxPreview != nil || c.service != nil {
			t.Fatal("detection read history or started engine")
		}
	}
	empty, nonempty := 0, 1
	check("", &empty)
	if _, err := os.Stat(c.settings.StorageDir); !os.IsNotExist(err) {
		t.Fatal("detection created target")
	}
	history := &cacheproxy.SongUsageImport{}
	history.Observe(99, time.Now().UnixMilli())
	if err := cacheproxy.ImportSongUsage(t.Context(), c.settings.StorageDir, history, false); err != nil {
		t.Fatal(err)
	}
	check("", &nonempty)
	if err := os.Rename(c.settings.StorageDir, c.settings.StorageDir+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	c.inventory.CoverageKnown = true
	check("", nil)
	check(filepath.Join(t.TempDir(), "new-target"), nil)
}

func TestVRCXDiscardExpiryAndEmptySource(t *testing.T) {
	c := testConsole(t)
	source := vrcxFixture(t)
	root := c.settings.StorageDir
	p := previewVRCX(t, c, source, root)
	if p.StorageDir != root {
		t.Fatal(p.StorageDir)
	}
	if w := vrcxRequest(t, c, "discard", map[string]string{"id": p.ID}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := vrcxRequest(t, c, "apply", map[string]string{"id": p.ID}); w.Code != 409 {
		t.Fatal("discarded preview usable")
	}
	p = previewVRCX(t, c, source, root)
	c.vrcxPreview.expires = time.Now().Add(-time.Second)
	if w := vrcxRequest(t, c, "apply", map[string]string{"id": p.ID}); w.Code != 409 {
		t.Fatal("expired preview usable")
	}
	p = previewVRCX(t, c, source, root)
	if w := vrcxRequest(t, c, "apply", map[string]string{"id": p.ID}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if count, err := cacheproxy.ReadSongUsageCount(context.Background(), root); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if _, err := c.inspectStorage(root); err != nil {
		t.Fatal("cannot reuse imported target", err)
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("DELETE FROM gamelog_video_play"); err != nil {
		t.Fatal(err)
	}
	p = previewVRCX(t, c, source, root)
	if p.ID != "" || p.Events != 0 {
		t.Fatal(p)
	}
	if w := vrcxRequest(t, c, "apply", map[string]any{"id": "", "overwrite": true}); w.Code != 409 {
		t.Fatal("empty result usable")
	}
	if count, _ := cacheproxy.ReadSongUsageCount(context.Background(), root); count != 2 {
		t.Fatal("empty result cleared statistics")
	}
}

func TestVRCXGatesDetectionAndPicker(t *testing.T) {
	c := testConsole(t)
	t.Setenv("APPDATA", t.TempDir())
	for _, action := range []string{"detect", "select", "preview", "apply", "discard"} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/vrcx/"+action, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(action, w.Code)
		}
		r = httptest.NewRequest("POST", "http://"+c.address+"/api/vrcx/"+action, strings.NewReader("{}"))
		r.Header.Set("X-SWD-Token", c.token)
		r.Header.Set("Origin", "http://untrusted.invalid")
		w = httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(action, w.Code)
		}
	}
	w := vrcxRequest(t, c, "detect", map[string]any{})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"found":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if c.vrcxPreview != nil || c.service != nil {
		t.Fatal("detection read history or started service")
	}
	source := vrcxFixture(t)
	c.databasePicker = func(_ context.Context, title, initial string) (string, error) {
		if !strings.Contains(title, "VRCX") {
			t.Fatal(title)
		}
		return source, nil
	}
	w = vrcxRequest(t, c, "select", map[string]string{"path": source})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	c.terms = termsReceipt{}
	w = vrcxRequest(t, c, "preview", map[string]string{"path": source})
	if w.Code != 428 {
		t.Fatal("terms bypass", w.Code)
	}
}

// Opt-in developer check: source stays read-only and all writes use t.TempDir.
func TestVRCXLocalReadOnlyIntegration(t *testing.T) {
	path := os.Getenv("SWD_TEST_VRCX")
	if path == "" {
		t.Skip("explicit local source not provided")
	}
	c := testConsole(t)
	hashes := map[string][32]byte{}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err == nil {
			hashes[suffix] = sha256.Sum256(data)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	p := previewVRCX(t, c, path, "")
	if p.Events == 0 {
		t.Fatal("no local recognizable events")
	}
	if w := vrcxRequest(t, c, "apply", map[string]string{"id": p.ID}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if count, err := cacheproxy.ReadSongUsageCount(context.Background(), c.settings.StorageDir); err != nil || count != p.Songs {
		t.Fatal(count, err)
	}
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = c.settings.StorageDir
	cfg.CacheUnlimited = true
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := engine.SongUsageCount(context.Background()); err != nil || count != p.Songs {
		t.Fatal("reopened engine", count, err)
	}
	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, before := range hashes {
		data, err := os.ReadFile(path + suffix)
		if err != nil || sha256.Sum256(data) != before {
			t.Fatal("source changed during read-only check; cannot confirm stable source", suffix, err)
		}
	}
	t.Logf("read-only source: %d rows, %d recognized, %d deduplicated, %d songs, %d skipped; isolated target verified", p.Total, p.Recognized, p.Events, p.Songs, p.Skipped)
}
