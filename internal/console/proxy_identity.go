package console

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Keep the random HMAC key separate from public channel IDs and sample files.
// Changing endpoint or either credential changes the identity; rebuilding a
// pool or restarting with the same configuration does not.
func (c *Console) proxyIdentity(s Settings) (string, error) {
	path := c.configPath + ".channel-key"
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return "", err
		}
		f, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(createErr, os.ErrExist) {
			key, err = os.ReadFile(path)
		} else if createErr != nil {
			return "", createErr
		} else {
			_, err = f.Write(key)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return "", err
	}
	if len(key) != 32 {
		return "", errors.New("invalid proxy channel identity key")
	}
	data, err := json.Marshal([]string{"socks5-channel-v1", s.SOCKS5Address, s.SOCKS5Username, s.SOCKS5Password})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return "stable-v1-" + hex.EncodeToString(mac.Sum(nil)), nil
}
