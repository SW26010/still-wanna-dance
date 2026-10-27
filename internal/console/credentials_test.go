package console

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUnsupportedProxyEnvelopeStaysUnavailableAndPreserved(t *testing.T) {
	for _, raw := range []string{`null`, `{"version":999,"protection":"future","ciphertext":"keep"}`, `{"version":1,"protection":"dpapi-current-user","ciphertext":"!invalid-base64"}`} {
		t.Run(raw, func(t *testing.T) {
			c := testConsole(t)
			_, fields, err := readConfigFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			fields["upstreamMode"] = json.RawMessage(`"proxy"`)
			fields["socks5Address"] = json.RawMessage(`"127.0.0.1:1080"`)
			fields["proxySecret"] = json.RawMessage(raw)
			b, _ := json.Marshal(fields)
			if err := os.WriteFile(c.configPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			if loaded.settings.proxySecretError == "" {
				t.Fatal("unsupported secret treated as absent")
			}
			if conn, err := loaded.upstreamDial(context.Background(), "tcp", "example.invalid:443"); err == nil {
				conn.Close()
				t.Fatal("unavailable proxy allowed traffic")
			}
			if err := loaded.saveSettings(loaded.savedSettings, true, false); err != nil {
				t.Fatal(err)
			}
			_, saved, err := readConfigFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			json.Unmarshal([]byte(raw), &before)
			json.Unmarshal(saved["proxySecret"], &after)
			a, _ := json.Marshal(before)
			b, _ = json.Marshal(after)
			if string(a) != string(b) {
				t.Fatal("unsupported ciphertext lost")
			}
			// Even public status must contain only the fixed diagnostic, not envelopes.
			w := httptest.NewRecorder()
			loaded.ServeHTTP(w, httptest.NewRequest("GET", "http://"+loaded.address+"/api/status", nil))
			if strings.Contains(w.Body.String(), "ciphertext") {
				t.Fatal("status exposed protected envelope")
			}
		})
	}
}
