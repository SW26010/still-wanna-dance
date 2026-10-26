package console

import (
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/workfiles"
)

func TestAbandonedTermsWriteNeverGrantsAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	lease, err := desktop.LockConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	p := apppaths.ForConfig(path)
	// Even a valid receipt is only effective once atomically published.
	c, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := os.Rename(p.Terms, filepath.Join(p.Root, workfiles.TempName(p.Terms))); err != nil {
		t.Fatal(err)
	}
	if err := lease.CleanWorkFiles(); err != nil {
		t.Fatal(err)
	}
	next, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.termsAccepted() {
		t.Fatal("temporary receipt granted acceptance")
	}
}
