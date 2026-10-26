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
	return termsEnvelopeBytes(t, protectedReceiptSlot(t, receipt))
}

func protectedReceiptSlot(t *testing.T, receipt protectedTerms) json.RawMessage {
	t.Helper()
	plain, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := defaultTermsProtection.protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cipher)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func termsEnvelopeBytes(t *testing.T, receipts ...json.RawMessage) []byte {
	t.Helper()
	b, err := json.Marshal(termsEnvelope{2, "dpapi-current-user", receipts})
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
	moved, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "moved.json"), c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	moved.settings.AutoOpenWebUI = false
	if err := moved.save(moved.settings); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(moved.files().StateDir, 0700); err != nil {
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
	var cipher []byte
	if err := json.Unmarshal(envelope.Receipts[0], &cipher); err != nil {
		t.Fatal(err)
	}
	// A valid old single-slot envelope is deliberately unsupported.
	single, _ := json.Marshal(map[string]any{"format": 1, "protection": "dpapi-current-user", "ciphertext": cipher})
	cipher[len(cipher)/2] ^= 1
	envelope.Receipts[0], _ = json.Marshal(cipher)
	tampered, _ := json.Marshal(envelope)
	legacy, _ := json.Marshal(valid.termsReceipt)
	tooMany := make([]json.RawMessage, maxTermsReceipts+1)
	for i := range tooMany {
		tooMany[i] = protectedReceiptSlot(t, valid)
	}
	cases := map[string][]byte{
		"legacy": legacy, "truncated": b[:len(b)/2], "tampered": tampered,
		"single slot format": single,
		"invalid cipher":     []byte(`{"format":2,"protection":"dpapi-current-user","receipts":["YWJj"]}`),
		"future format":      bytes.Replace(b, []byte(`"format":2`), []byte(`"format":3`), 1),
		"wrong protection":   bytes.Replace(b, []byte("dpapi-current-user"), []byte("plaintext"), 1),
		"no slots":           termsEnvelopeBytes(t), "too many slots": termsEnvelopeBytes(t, tooMany...),
		"empty": nil, "oversized": bytes.Repeat([]byte("x"), maxTermsBytes+1),
		"version": protectedReceiptBytes(t, oldVersion), "hash": protectedReceiptBytes(t, oldHash),
		"time": protectedReceiptBytes(t, zeroTime), "purpose": protectedReceiptBytes(t, wrongPurpose),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(moved.files().Terms, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := newConsoleWithWorkForTest(t, moved.configPath, moved.address)
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
	if err := os.WriteFile(moved.files().Terms, single, 0600); err != nil {
		t.Fatal(err)
	}
	if err := moved.loadTerms(); err != nil || moved.termsAccepted() {
		t.Fatal("old encrypted format must require new consent", err)
	}
	if err := moved.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	receipts, _, err := readTermsReceipts(moved.files().Terms)
	if err != nil || len(receipts) != 1 || !moved.termsAccepted() {
		t.Fatal("explicit consent did not replace the unsupported format", err)
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
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
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
	if err := os.MkdirAll(c.files().Terms, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.loadTerms(); err == nil {
		t.Fatal("ordinary I/O error was hidden")
	}
}
