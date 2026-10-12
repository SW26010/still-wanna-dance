package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const nodeFixture = "proxies: [{type: ss, server: private.example, port: 1234, cipher: aes-128-gcm, password: private-password}]"

func TestProxyImportDraftPrivateBoundedAndStale(t *testing.T) {
	c := testConsole(t)
	before, _ := os.ReadFile(c.configPath)
	r, err := c.importProxyNodes([]byte(nodeFixture))
	if err != nil || r.ID == "" || r.Accepted != 1 {
		t.Fatalf("import: %v", err)
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(before, after) || c.settings.nodeSnapshot != "" || c.savedSettings.nodeSnapshot != "" {
		t.Fatal("preview saved or activated")
	}
	s := c.savedSettings
	s.nodeImportID = r.ID
	merged, err := c.mergeProxyNodes(s, c.savedSettings)
	if err != nil || merged.nodeSnapshot == "" {
		t.Fatal("draft not merged")
	}
	key := []byte(strings.Repeat("k", 32))
	p := nodeDocument(merged.nodeSnapshot).Nodes[0]
	id := nodeIdentity(key, p)
	p["password"] = "different"
	if id == nodeIdentity(key, p) {
		t.Fatal("credential change reused identity")
	}
	version, _ := c.setupSettingsVersion(c.savedSettings)
	changed, _ := c.setupSettingsVersion(merged)
	if version == changed {
		t.Fatal("wizard verifier ignores nodes")
	}
	public, _ := json.Marshal(r)
	if strings.Contains(string(public), "private") {
		t.Fatal("report exposed nodes")
	}
	old := c.savedSettings
	old.nodeSnapshot = "changed"
	if _, err := c.mergeProxyNodes(s, old); err == nil {
		t.Fatal("stale snapshot accepted")
	}
	draft := c.proxyImports[r.ID]
	draft.Expires = time.Now().Add(-time.Second)
	c.proxyImports[r.ID] = draft
	if _, err := c.mergeProxyNodes(s, c.savedSettings); err == nil {
		t.Fatal("expired draft accepted")
	}
}

func TestProxyImportEndpointAuthorizationAndBudgets(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		path, body, token string
		want              int
	}{
		{"/api/proxy/import", nodeFixture, "", 403},
		{"/api/proxy/import", nodeFixture, c.token, 200},
		{"/api/proxy/import", strings.Repeat("x", (2<<20)+1), c.token, 400},
		{"/api/settings", strings.Repeat(" ", 8193) + "{}", c.token, 400},
	} {
		r := httptest.NewRequest("POST", "http://"+c.address+tc.path, strings.NewReader(tc.body))
		r.Header.Set("X-SWD-Token", tc.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestUnavailableProxyNodesPreserveEnvelope(t *testing.T) {
	c := testConsole(t)
	_, fields, _ := readConfigFile(c.configPath)
	fields["proxyNodes"] = json.RawMessage(`{"version":999,"protection":"future","ciphertext":"private"}`)
	b, _ := json.Marshal(fields)
	if err := os.WriteFile(c.configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.nodeSecretError == "" {
		t.Fatal("unreadable nodes treated as empty")
	}
	if err := loaded.saveSettings(loaded.savedSettings, true); err != nil {
		t.Fatal(err)
	}
	_, after, _ := readConfigFile(c.configPath)
	var a, z any
	json.Unmarshal(fields["proxyNodes"], &a)
	json.Unmarshal(after["proxyNodes"], &z)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(z)
	if !bytes.Equal(x, y) {
		t.Fatal("unreadable ciphertext lost")
	}
	w := httptest.NewRecorder()
	loaded.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	if strings.Contains(w.Body.String(), "ciphertext") || strings.Contains(w.Body.String(), "private") {
		t.Fatal("status leaked envelope")
	}
}
