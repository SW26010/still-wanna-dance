package console

import (
	"encoding/json"
	"errors"
)

const unavailableProxySecret = "已保存的代理凭据无法解密，请重新输入密码并保存、重启；原密文仍保留"

func loadProxySecret(fields map[string]json.RawMessage, s *Settings) {
	raw := fields["proxySecret"]
	if len(raw) == 0 {
		return
	}
	s.proxySecret = string(raw)
	s.proxySecretError = unavailableProxySecret
	plain, err := decodeProtectedEnvelope(raw)
	if err != nil {
		return
	}
	defer clear(plain)
	if len(plain) == 0 || len(plain) > 255 {
		return
	}
	s.SOCKS5Password = string(plain)
	s.proxySecret = ""
	s.proxySecretError = ""
}

func storedProxySecret(s Settings) (json.RawMessage, error) {
	if s.proxySecretError != "" {
		if s.proxySecret == "" {
			return nil, errors.New(unavailableProxySecret)
		}
		return json.RawMessage(s.proxySecret), nil
	}
	if s.SOCKS5Password == "" {
		return nil, nil
	}
	plain := []byte(s.SOCKS5Password)
	defer clear(plain)
	return encodeProtectedEnvelope(plain)
}
