package console

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Keep the random HMAC key separate from public channel IDs and sample files.
// Changing endpoint or either credential changes the identity; rebuilding a
// pool or restarting with the same configuration does not.
func (c *Console) proxyIdentity(s Settings) (string, error) {
	path := c.configPath + ".channel-key"
	key, err := loadProxyIdentityKey(path)
	if err != nil {
		// An empty identity makes the pool use a process-local ID, which cannot
		// claim persisted samples. Persistence must not prevent proxy use.
		slog.Warn("proxy_channel_identity_unavailable", "error", err)
		return "", nil
	}
	data, err := json.Marshal([]string{"socks5-channel-v1", s.SOCKS5Address, s.SOCKS5Username, s.SOCKS5Password})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return "stable-v1-" + hex.EncodeToString(mac.Sum(nil)), nil
}

func loadProxyIdentityKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) != 32 {
		slog.Warn("proxy_channel_identity_corrupt", "path", path)
		backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err := os.Rename(path, backup); err != nil {
			return nil, err
		}
		err = os.ErrNotExist
	}
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(createErr, os.ErrExist) {
			key, err = os.ReadFile(path)
		} else if createErr != nil {
			return nil, createErr
		} else {
			_, err = f.Write(key)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid proxy channel identity key")
	}
	return key, nil
}
