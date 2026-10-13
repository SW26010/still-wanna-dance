package console

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedNodesSaveRestartMoveAndFailedReplacement(t *testing.T) {
	c := testConsole(t)
	r, err := c.importProxyNodes([]byte(nodeFixture))
	if err != nil {
		t.Fatal(err)
	}
	s := c.savedSettings
	s.nodeImportID = r.ID
	if err := c.saveSettings(s, true); err != nil {
		t.Fatal(err)
	}
	if c.settings == c.savedSettings || c.settings.nodeSnapshot != "" || c.savedSettings.nodeImportID != "" {
		t.Fatal("incorrect staged lifecycle")
	}
	b, _ := os.ReadFile(c.configPath)
	if bytes.Contains(b, []byte("private")) || bytes.Contains(b, []byte("aes-128-gcm")) {
		t.Fatal("plaintext persisted")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, b, 0600)
	loaded, err := New(path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.nodeSnapshot != c.savedSettings.nodeSnapshot {
		t.Fatal("move changed snapshot")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	var envelope proxySecretEnvelope
	json.Unmarshal(fields["proxyNodes"], &envelope)
	envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 0xff
	fields["proxyNodes"], _ = json.Marshal(envelope)
	b, _ = json.Marshal(fields)
	os.WriteFile(path, b, 0600)
	broken, err := New(path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	if broken.settings.nodeSecretError == "" || broken.settings.nodeSnapshot != "" {
		t.Fatal("tampered nodes accepted")
	}
	// The same config transaction saves settings and the encrypted snapshot.
	old := c.savedSettings
	r, _ = c.importProxyNodes([]byte("proxies: [{type: ss, server: replacement.example, port: 1234, cipher: aes-128-gcm, password: replacement}]"))
	s = old
	s.nodeImportID = r.ID
	_, fields, _ = readConfigFile(c.configPath)
	fields["schemaVersion"] = json.RawMessage(`999`)
	b, _ = json.Marshal(fields)
	os.WriteFile(c.configPath, b, 0600)
	if c.saveSettings(s, true) == nil || c.savedSettings != old {
		t.Fatal("failed save published replacement")
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(b, after) {
		t.Fatal("failed save changed disk")
	}
}

func TestLegacyNodeSnapshotPreservedUntilExplicitReimport(t *testing.T) {
	c := testConsole(t)
	plain, err := os.ReadFile("testdata/proxy-nodes-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := protectUserData(plain)
	if err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(proxySecretEnvelope{1, "dpapi-current-user", cipher})
	s := c.savedSettings
	loadProxyNodes(map[string]json.RawMessage{"proxyNodes": envelope}, &s)
	if s.nodeSnapshot != "" || s.nodeSecretError == "" {
		t.Fatal("legacy silently reinterpreted")
	}
	stored, err := storedProxyNodes(s)
	if err != nil || !bytes.Equal(stored, envelope) {
		t.Fatal("legacy ciphertext changed", err)
	}
	c.savedSettings = s
	result, err := c.importProxyNodes([]byte(nodeFixture))
	if err != nil {
		t.Fatal(err)
	}
	s.nodeImportID = result.ID
	next, err := c.mergeProxyNodes(s, c.savedSettings)
	if err != nil || nodeDocument(next.nodeSnapshot).Version != proxyNodeDocumentVersion || next.nodeSecretError != "" {
		t.Fatal("explicit reimport failed", err)
	}
}

func TestSetupAppliesNodeDraftOnlyAtFinalConfirmation(t *testing.T) {
	if !consoleMihomoProcess(t) {
		return
	}
	c := testConsole(t)
	result, err := c.importProxyNodes([]byte(nodeFixture))
	if err != nil {
		t.Fatal(err)
	}
	plan := setupTestPlan(c)
	plan.UpstreamMode, plan.ProxyBackend, plan.ProxyImportID = "proxy", "mihomo", result.ID
	preview := plan
	preview.Page = "proxy"
	before, _ := os.ReadFile(c.configPath)
	if _, err := c.validateSetupPlan(preview); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(c.configPath)
	if !bytes.Equal(before, after) || c.savedSettings.nodeSnapshot != "" {
		t.Fatal("validation persisted nodes")
	}
	applied, err := c.applySetupPlan(plan)
	if err != nil || !applied.ConfigSaved || applied.Phase != "restart" {
		t.Fatal("wizard did not save nodes", err, applied.Phase)
	}
	if c.savedSettings.nodeSnapshot == "" || c.settings.nodeSnapshot != "" {
		t.Fatal("wizard activated draft early")
	}
	receipt, _ := os.ReadFile(c.files().State("setup-apply"))
	if bytes.Contains(receipt, []byte("private")) || bytes.Contains(receipt, []byte(result.ID)) {
		t.Fatal("receipt retained private nodes or draft token")
	}
	if _, err := c.validateSetupPlan(preview); err == nil {
		t.Fatal("stale wizard ignored changed snapshot")
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	loaded.resumeSetupApplication(func(string) error { t.Fatal("unexpected media import"); return nil })
	if loaded.setupApplication.Phase != "completed" || loaded.settings.nodeSnapshot != c.savedSettings.nodeSnapshot || len(loaded.client.Transport.(*mihomoTransport).proxies) != 1 {
		t.Fatal("node restart continuation failed")
	}
}
