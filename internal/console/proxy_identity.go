package console

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

type proxyChannelIdentity struct {
	once       sync.Once
	key        []byte
	persistent bool
	err        error
}

// All consumers share one preparation result, including a failed save. A later
// read must not promote a key whose write/Sync was unsuccessful in this instance.
// Return a copy because callers clear their key material after fingerprinting.
func (c *Console) channelIdentityKey() ([]byte, bool, error) {
	id := &c.channelIdentity
	id.once.Do(func() {
		key, err := loadProxyIdentityKey(c.files().ChannelKey)
		if err == nil {
			id.key, id.persistent = key, true
			return
		}
		slog.Warn("proxy_channel_identity_unavailable", "error", err)
		id.key = make([]byte, 32)
		_, id.err = rand.Read(id.key)
	})
	return slices.Clone(id.key), id.persistent, id.err
}

func loadProxyIdentityKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) != 32 {
		slog.Warn("proxy_channel_identity_corrupt", "path", path)
		return nil, errors.New("通道身份密钥损坏；原文件保留，请从备份恢复；本次仅使用临时通道身份")
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
			if err == nil {
				err = f.Sync()
			}
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
