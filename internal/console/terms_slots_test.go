package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/legal"
	"still-wanna-dance/internal/workfiles"
)

func TestTermsSlotsNewestMatch(t *testing.T) {
	older := protectedTerms{termsPurpose, termsReceipt{legal.Version, legal.Hash(), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}}
	newer := older
	newer.AcceptedAt = older.AcceptedAt.Add(-time.Hour) // a clock correction must not change slot order
	stale, wrongPurpose := older, older
	stale.Version = "previous-terms"
	wrongPurpose.Purpose = "another-purpose"
	badCipher, err := defaultTermsProtection.protect([]byte("invalid-json"))
	if err != nil {
		t.Fatal(err)
	}
	badPayload, _ := json.Marshal(badCipher)
	for _, tc := range []struct {
		name     string
		slots    []json.RawMessage
		want     termsReceipt
		decrypts int
	}{
		{"newest first", []json.RawMessage{protectedReceiptSlot(t, older), protectedReceiptSlot(t, newer)}, newer.termsReceipt, 1},
		{"skip unusable slots", []json.RawMessage{
			protectedReceiptSlot(t, older), json.RawMessage(`"!"`), json.RawMessage(`null`), json.RawMessage(`{}`),
			json.RawMessage(`"YWJj"`), badPayload, protectedReceiptSlot(t, wrongPurpose), protectedReceiptSlot(t, stale),
		}, older.termsReceipt, 5},
		{"no match", []json.RawMessage{protectedReceiptSlot(t, stale), json.RawMessage(`"YWJj"`)}, termsReceipt{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Console{configPath: filepath.Join(t.TempDir(), "config.json")}
			before := termsEnvelopeBytes(t, tc.slots...)
			if err := workfiles.Write(c.files().Terms, before); err != nil {
				t.Fatal(err)
			}
			if err := c.loadTerms(); err != nil {
				t.Fatal(err)
			}
			if c.terms != tc.want || c.termsNeedsConfirmation == tc.want.valid() {
				t.Fatalf("unexpected loaded receipt: %+v, needsConfirmation=%t", c.terms, c.termsNeedsConfirmation)
			}
			decrypts := 0
			c.termsProtection.unprotect = func(cipher []byte) ([]byte, error) {
				decrypts++
				return defaultTermsProtection.unprotect(cipher)
			}
			if got := c.findAcceptedTerms(tc.slots); got != tc.want || decrypts != tc.decrypts {
				t.Fatalf("receipt=%+v, decryptions=%d; want %+v, %d", got, decrypts, tc.want, tc.decrypts)
			}
			after, err := os.ReadFile(c.files().Terms)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("reading slots changed the saved receipts", err)
			}
		})
	}
}

func TestTermsSlotsRetainIndependentEnvironments(t *testing.T) {
	// Simulate independent decryption contexts while retaining real encryption.
	// This verifies slot management, not OS cross-user/machine DPAPI behavior.
	base := defaultTermsProtection
	t.Cleanup(func() { defaultTermsProtection = base })
	active := 0
	owners := make(map[string]int)
	defaultTermsProtection = dataProtection{
		protect: func(plain []byte) ([]byte, error) {
			cipher, err := base.protect(plain)
			if err == nil {
				owners[string(cipher)] = active
			}
			return cipher, err
		},
		unprotect: func(cipher []byte) ([]byte, error) {
			if owner, ok := owners[string(cipher)]; !ok || owner != active {
				return nil, errors.New("another environment")
			}
			return base.unprotect(cipher)
		},
	}
	c := &Console{configPath: filepath.Join(t.TempDir(), "config.json")}
	var previous []json.RawMessage
	for active = 0; active <= maxTermsReceipts; active++ {
		if err := c.loadTerms(); err != nil || c.termsAccepted() {
			t.Fatal("new environment inherited another confirmation", err)
		}
		if err := c.acceptTerms(); err != nil || !c.termsAccepted() {
			t.Fatal("could not save independent confirmation", err)
		}
		slots, _, err := readTermsReceipts(c.files().Terms)
		if err != nil || len(slots) != min(active+1, maxTermsReceipts) {
			t.Fatal("unexpected slot count", len(slots), err)
		}
		if len(previous) == maxTermsReceipts {
			previous = previous[1:]
		}
		for i := range previous {
			if !bytes.Equal(slots[i], previous[i]) {
				t.Fatal("new confirmation changed a retained ciphertext")
			}
		}
		previous = slots
	}
	before, err := os.ReadFile(c.files().Terms)
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []int{0, 1, maxTermsReceipts} {
		active = environment
		if err := c.loadTerms(); err != nil || c.termsAccepted() != (environment != 0) {
			t.Fatalf("environment %d: unexpected retained confirmation: %v", environment, err)
		}
		if environment != 0 {
			acceptedAt := c.terms.AcceptedAt
			for range 2 {
				if err := c.acceptTerms(); err != nil || c.terms.AcceptedAt != acceptedAt {
					t.Fatal("repeated confirmation replaced the existing receipt", err)
				}
			}
		}
	}
	after, err := os.ReadFile(c.files().Terms)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reading or repeating confirmation consumed a slot", err)
	}
}

func TestTermsSlotsPreserveHistoricalReceipts(t *testing.T) {
	c := &Console{configPath: filepath.Join(t.TempDir(), "config.json")}
	var slots []json.RawMessage
	for i := range maxTermsReceipts {
		// Earlier saves deliberately have later timestamps; eviction follows position.
		receipt := protectedTerms{termsPurpose, termsReceipt{fmt.Sprintf("old-%d", i), legal.Hash(), time.Date(2040-i, 1, 1, 0, 0, 0, 0, time.UTC)}}
		slots = append(slots, protectedReceiptSlot(t, receipt))
	}
	if err := workfiles.Write(c.files().Terms, termsEnvelopeBytes(t, slots...)); err != nil {
		t.Fatal(err)
	}
	if err := c.loadTerms(); err != nil || c.termsAccepted() {
		t.Fatal("historical consent granted current consent", err)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	saved, _, err := readTermsReceipts(c.files().Terms)
	if err != nil || len(saved) != maxTermsReceipts {
		t.Fatal("unexpected slot count", len(saved), err)
	}
	for i := range maxTermsReceipts - 1 {
		if !bytes.Equal(saved[i], slots[i+1]) {
			t.Fatal("historical receipts were discarded or ordered by timestamp")
		}
	}
	if err := c.loadTerms(); err != nil || !c.termsAccepted() {
		t.Fatal("new consent was not durable", err)
	}
}

func TestTermsSlotsSaveFailurePreservesExistingFile(t *testing.T) {
	for _, failure := range []string{"protect", "write", "size"} {
		t.Run(failure, func(t *testing.T) {
			c := &Console{configPath: filepath.Join(t.TempDir(), "config.json")}
			before := termsEnvelopeBytes(t, json.RawMessage(`"YWJj"`))
			if failure == "size" {
				slot, _ := json.Marshal(string(bytes.Repeat([]byte("x"), maxTermsBytes-128)))
				before = termsEnvelopeBytes(t, slot)
			}
			if err := workfiles.Write(c.files().Terms, before); err != nil {
				t.Fatal(err)
			}
			if err := c.loadTerms(); err != nil {
				t.Fatal(err)
			}
			if failure == "protect" {
				c.termsProtection.protect = func([]byte) ([]byte, error) { return nil, errors.New("protection unavailable") }
			}
			if failure == "write" {
				if err := os.WriteFile(filepath.Join(c.files().StateDir, workfiles.TempName(c.files().Terms)), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.acceptTerms(); err == nil || c.termsAccepted() || !c.termsNeedsConfirmation {
				t.Fatal("failed save granted consent", err)
			}
			after, err := os.ReadFile(c.files().Terms)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed save changed existing ciphertexts", err)
			}
		})
	}
}
