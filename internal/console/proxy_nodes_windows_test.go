package console

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProxyConfigurationDisplayMetadataAndOriginalProtection(t *testing.T) {
	c := testConsole(t)
	result, err := c.editor.importProxyNodes(context.Background(), []byte(nodeFixture), `C:\private\机场配置.yaml`)
	if err != nil || result.Name != "机场配置.yaml" || result.ImportedAt.IsZero() {
		t.Fatal("invalid imported metadata", err)
	}
	s := c.editor.savedSettings
	s.nodeImportID = result.ID
	if err := c.editor.saveSettings(s, true, nil); err != nil {
		t.Fatal(err)
	}
	data, fields, err := readConfigFile(c.configPath)
	if err != nil || !bytes.Contains(data, []byte("机场配置.yaml")) || bytes.Contains(data, []byte("private")) {
		t.Fatal("unexpected display metadata or unprotected configuration")
	}
	plain, err := decodeProtectedEnvelope(fields["proxyNodes"])
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	if string(plain) != nodeFixture {
		t.Fatal("protected original configuration was rewritten")
	}
	var metadata proxyConfigurationMetadata
	if json.Unmarshal(fields["proxyConfigInfo"], &metadata) != nil || metadata.Name != result.Name || !metadata.ImportedAt.Equal(result.ImportedAt) {
		t.Fatal("display metadata did not persist")
	}
	var loaded Settings
	loadProxyNodes(fields, &loaded)
	if loaded.nodeSnapshot != nodeFixture || loaded.nodeInfo.Name != result.Name || !loaded.nodeInfo.ImportedAt.Equal(result.ImportedAt) || loaded.nodeInfo.Nodes != 1 {
		t.Fatal("metadata did not survive reload")
	}
	// Raw original documents from before metadata was recorded remain readable.
	envelope, err := encodeProtectedEnvelope([]byte(nodeFixture))
	if err != nil {
		t.Fatal(err)
	}
	var original Settings
	loadProxyNodes(map[string]json.RawMessage{"proxyNodes": envelope}, &original)
	if original.nodeSnapshot != nodeFixture || original.nodeInfo.Name != "" || !original.nodeInfo.ImportedAt.IsZero() {
		t.Fatal("missing original metadata was invented")
	}
	// Simulate settings loaded by a running instance, then replace metadata only.
	c.settings = c.editor.savedSettings
	result, err = c.editor.importProxyNodes(context.Background(), []byte(nodeFixture), "renamed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s = c.editor.savedSettings
	s.nodeImportID = result.ID
	if err := c.editor.saveSettings(s, true, nil); err != nil {
		t.Fatal(err)
	}
	if c.settings != c.editor.savedSettings {
		t.Fatal("display metadata incorrectly requires restart")
	}
}

func TestProtectedNodesSaveRestartMoveAndFailedReplacement(t *testing.T) {
	c := testConsole(t)
	r, err := c.editor.importProxyNodes(context.Background(), []byte(nodeFixture), "fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := c.editor.savedSettings
	s.nodeImportID = r.ID
	if err := c.editor.saveSettings(s, true, nil); err != nil {
		t.Fatal(err)
	}
	if c.settings == c.editor.savedSettings || c.settings.nodeSnapshot != "" || c.editor.savedSettings.nodeImportID != "" {
		t.Fatal("incorrect staged lifecycle")
	}
	b, _ := os.ReadFile(c.configPath)
	if bytes.Contains(b, []byte("private")) || bytes.Contains(b, []byte("aes-128-gcm")) {
		t.Fatal("plaintext persisted")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, b, 0600)
	loaded, err := newConsoleWithWorkForTest(t, path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.nodeSnapshot != c.editor.savedSettings.nodeSnapshot {
		t.Fatal("move changed snapshot")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	var envelope protectedEnvelope
	json.Unmarshal(fields["proxyNodes"], &envelope)
	envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 0xff
	fields["proxyNodes"], _ = json.Marshal(envelope)
	b, _ = json.Marshal(fields)
	os.WriteFile(path, b, 0600)
	broken, err := newConsoleWithWorkForTest(t, path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	if broken.settings.nodeSecretError == "" || broken.settings.nodeSnapshot != "" {
		t.Fatal("tampered nodes accepted")
	}
	// The same config transaction saves settings and the encrypted snapshot.
	old := c.editor.savedSettings
	r, _ = c.editor.importProxyNodes(context.Background(), []byte("proxies: [{name: replacement, type: ss, server: replacement.example, port: 1234, cipher: aes-128-gcm, password: replacement}]"), "fixture.yaml")
	s = old
	s.nodeImportID = r.ID
	_, fields, _ = readConfigFile(c.configPath)
	fields["schemaVersion"] = json.RawMessage(`999`)
	b, _ = json.Marshal(fields)
	os.WriteFile(c.configPath, b, 0600)
	if c.editor.saveSettings(s, true, nil) == nil || c.editor.savedSettings != old {
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
	envelope, _ := json.Marshal(protectedEnvelope{1, "dpapi-current-user", cipher})
	s := c.editor.savedSettings
	loadProxyNodes(map[string]json.RawMessage{"proxyNodes": envelope}, &s)
	if s.nodeSnapshot != "" || s.nodeSecretError == "" {
		t.Fatal("legacy silently reinterpreted")
	}
	stored, err := storedProxyNodes(s)
	if err != nil || !bytes.Equal(stored, envelope) {
		t.Fatal("legacy ciphertext changed", err)
	}
	c.editor.savedSettings = s
	result, err := c.editor.importProxyNodes(context.Background(), []byte(nodeFixture), "fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s.nodeImportID = result.ID
	next, err := c.editor.mergeProxyNodes(s, c.editor.savedSettings)
	if err != nil || next.nodeSnapshot != nodeFixture || next.nodeSecretError != "" {
		t.Fatal("explicit reimport failed", err)
	}
}
