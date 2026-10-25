package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/legal"
)

func protectedReceiptBytes(t *testing.T, receipt protectedTerms) []byte {
	t.Helper()
	plain, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := defaultTermsProtection.protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(termsEnvelope{1, "dpapi-current-user", cipher})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTermsProtectedMoveAndInvalidReceipts(t *testing.T) {
	c := testConsole(t)
	b, err := os.ReadFile(c.files().Terms)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{legal.Version, legal.Hash(), "acceptedAt", termsPurpose} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("receipt exposes %s", secret)
		}
	}
	// Copy the receipt into a renamed custom configuration's companion directory.
	moved, err := New(filepath.Join(t.TempDir(), "moved.json"), c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	moved.settings.AutoOpenWebUI = false
	if err := moved.save(moved.settings); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(moved.files().Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved.files().Terms, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := moved.loadTerms(); err != nil {
		t.Fatal(err)
	}
	if !moved.termsAccepted() {
		t.Fatal("same-user move lost acceptance")
	}

	valid := protectedTerms{termsPurpose, termsReceipt{legal.Version, legal.Hash(), time.Now().UTC()}}
	oldVersion, oldHash, zeroTime, wrongPurpose := valid, valid, valid, valid
	oldVersion.Version = "old"
	oldHash.Hash = "wrong"
	zeroTime.AcceptedAt = time.Time{}
	wrongPurpose.Purpose = "proxy-secret"
	var envelope termsEnvelope
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Ciphertext[len(envelope.Ciphertext)/2] ^= 1
	tampered, _ := json.Marshal(envelope)
	legacy, _ := json.Marshal(valid.termsReceipt)
	cases := map[string][]byte{
		"legacy": legacy, "truncated": b[:len(b)/2], "tampered": tampered,
		"invalid cipher":   []byte(`{"format":1,"protection":"dpapi-current-user","ciphertext":"YWJj"}`),
		"future format":    []byte(`{"format":2,"protection":"dpapi-current-user","ciphertext":"YWJj"}`),
		"wrong protection": []byte(`{"format":1,"protection":"plaintext","ciphertext":"YWJj"}`),
		"empty":            nil, "oversized": bytes.Repeat([]byte("x"), maxTermsBytes+1),
		"version": protectedReceiptBytes(t, oldVersion), "hash": protectedReceiptBytes(t, oldHash),
		"time": protectedReceiptBytes(t, zeroTime), "purpose": protectedReceiptBytes(t, wrongPurpose),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(moved.files().Terms, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := New(moved.configPath, moved.address)
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			if loaded.termsAccepted() {
				t.Fatal("invalid receipt accepted")
			}
			if !loaded.OpenWebUIOnLaunch() || loaded.settings.AutoOpenWebUI {
				t.Fatal("invalid receipt should open confirmation without changing the preference")
			}
			loaded.settings.AutoAccelerate, loaded.settings.QueueWithGateway = true, true
			loaded.AutoStart()
			if loaded.service != nil || loaded.httpServer != nil || loaded.queueCancel != nil {
				t.Fatal("invalid receipt started services")
			}
			w := httptest.NewRecorder()
			loaded.ServeHTTP(w, httptest.NewRequest("GET", "http://"+loaded.address+"/api/status", nil))
			if w.Code != 428 {
				t.Fatalf("gate status %d", w.Code)
			}
			after, err := os.ReadFile(loaded.files().Terms)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("invalid receipt rewritten without consent")
			}
		})
	}
	if err := os.Remove(moved.files().Terms); err != nil {
		t.Fatal(err)
	}
	if err := moved.loadTerms(); err != nil {
		t.Fatal(err)
	}
	if moved.termsAccepted() {
		t.Fatal("missing receipt retained stale acceptance")
	}
}

func TestTermsProtectFailureAndReadError(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.termsProtection.protect = func([]byte) ([]byte, error) { return nil, errors.New("protection unavailable") }
	body, _ := json.Marshal(map[string]any{"version": legal.Version, "sha256": legal.Hash(), "agree": true, "contentRights": true})
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/terms/accept", strings.NewReader(string(body)))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 500 || c.termsAccepted() || c.networkDiagnostics != nil {
		t.Fatal("protection failure granted consent")
	}
	if _, err := os.Stat(c.files().Terms); !os.IsNotExist(err) {
		t.Fatal("failed protection wrote receipt")
	}
	if err := os.Mkdir(c.files().Terms, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.loadTerms(); err == nil {
		t.Fatal("ordinary I/O error was hidden")
	}
}
