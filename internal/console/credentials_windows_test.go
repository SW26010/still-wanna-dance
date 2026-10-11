package console

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedProxySecretMoveTamperAndRecovery(t *testing.T) {
	c := testConsole(t)
	s := c.settings
	s.UpstreamMode, s.SOCKS5Address = "socks5", "127.0.0.1:1080"
	s.SOCKS5Username, s.SOCKS5Password = "test-user", "never-persist-this-secret"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(s.SOCKS5Password)) || bytes.Contains(b, []byte(`"socks5Password"`)) {
		t.Fatal("plaintext persisted")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	moved, err := New(path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	if moved.settings.SOCKS5Password != s.SOCKS5Password {
		t.Fatal("move lost credential")
	}
	moved.Close()
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	var envelope proxySecretEnvelope
	json.Unmarshal(fields["proxySecret"], &envelope)
	envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 0xff
	fields["proxySecret"], _ = json.Marshal(envelope)
	b, _ = json.Marshal(fields)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	broken, err := New(path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	if broken.settings.proxySecretError == "" || broken.settings.SOCKS5Password != "" {
		t.Fatal("tampered credential accepted")
	}
	if conn, err := broken.upstreamDial(context.Background(), "tcp", "example.invalid:443"); err == nil {
		conn.Close()
		t.Fatal("invalid credential dialed")
	}
	next := broken.savedSettings
	next.QueuePrefetchCount++
	if err := broken.saveSettings(next, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	var retained map[string]json.RawMessage
	json.Unmarshal(after, &retained)
	var actual proxySecretEnvelope
	json.Unmarshal(retained["proxySecret"], &actual)
	if !bytes.Equal(actual.Ciphertext, envelope.Ciphertext) {
		t.Fatal("lost invalid ciphertext")
	}
	next.SOCKS5Password = "replacement-secret"
	if err := broken.saveSettings(next, false); err != nil {
		t.Fatal(err)
	}
	repaired, err := New(path, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer repaired.Close()
	if repaired.settings.proxySecretError != "" || repaired.settings.SOCKS5Password != next.SOCKS5Password {
		t.Fatal("explicit replacement failed")
	}
}
