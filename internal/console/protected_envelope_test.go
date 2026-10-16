package console

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestProxyEnvelopesRejectAndPreserveInvalidEncoding(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`{"version":2,"protection":"dpapi-current-user","ciphertext":"AA==","future":true}`,
		`{"version":1,"protection":"other","ciphertext":"AA=="}`,
		`{"version":1,"protection":"dpapi-current-user","ciphertext":"!invalid-base64"}`,
		`{"version":1,"protection":"dpapi-current-user","ciphertext":null}`,
		`{"version":1,"protection":"dpapi-current-user","ciphertext":"AA=="}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var s Settings
			fields := map[string]json.RawMessage{"proxySecret": json.RawMessage(raw), "proxyNodes": json.RawMessage(raw)}
			loadProxySecret(fields, &s)
			loadProxyNodes(fields, &s)
			if s.SOCKS5Password != "" || s.nodeSnapshot != "" || s.proxySecretError != unavailableProxySecret || s.nodeSecretError != unavailableProxyNodes {
				t.Fatal("invalid envelope changed payload or caller-specific diagnostic")
			}
			for name, store := range map[string]func(Settings) (json.RawMessage, error){"password": storedProxySecret, "nodes": storedProxyNodes} {
				stored, err := store(s)
				if err != nil || !bytes.Equal(stored, []byte(raw)) {
					t.Fatalf("%s lost the original envelope: %v", name, err)
				}
			}
		})
	}
}
