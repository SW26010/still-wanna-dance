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
