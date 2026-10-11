//go:build !windows

package console

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"os"
	"testing"
)

// Only the test executable uses an ephemeral key. This exercises authenticated
// storage and gates on Linux without shipping an alternative protection scheme.
func TestMain(m *testing.M) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	defaultTermsProtection = dataProtection{
		func(b []byte) ([]byte, error) { return aead.Seal(nil, nil, b, nil), nil },
		func(b []byte) ([]byte, error) { return aead.Open(nil, nil, b, nil) },
	}
	os.Exit(m.Run())
}

func TestNonWindowsProtectionUnsupported(t *testing.T) {
	if _, err := protectUserData([]byte("receipt")); err == nil {
		t.Fatal("production protection must fail closed")
	}
	if _, err := unprotectUserData([]byte("receipt")); err == nil {
		t.Fatal("production decryption must fail closed")
	}
}
