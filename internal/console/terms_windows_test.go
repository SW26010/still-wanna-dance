package console

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// Run the actual OS implementation in a fresh process under the same user.
// Cross-user/machine and roaming-profile behavior still require separate hosts.
func TestTermsDPAPIFreshProcess(t *testing.T) {
	if config := os.Getenv("SWD_TEST_TERMS_CONFIG"); config != "" {
		c := &Console{configPath: config}
		if err := c.loadTerms(); err != nil {
			t.Fatal(err)
		}
		if !c.termsAccepted() {
			t.Fatal("fresh process could not validate user receipt")
		}
		return
	}
	c := testConsole(t)
	receipts, _, err := readTermsReceipts(c.files().Terms)
	if err != nil {
		t.Fatal(err)
	}
	stale := protectedTerms{termsPurpose, c.terms}
	stale.Version = "previous-terms"
	for len(receipts) < maxTermsReceipts {
		receipts = append(receipts, protectedReceiptSlot(t, stale))
	}
	before := termsEnvelopeBytes(t, receipts...)
	if err := os.WriteFile(c.files().Terms, before, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTermsDPAPIFreshProcess$")
	cmd.Env = append(os.Environ(), "SWD_TEST_TERMS_CONFIG="+c.configPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	after, err := os.ReadFile(c.files().Terms)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("fresh process rewrote retained receipts", err)
	}
}
