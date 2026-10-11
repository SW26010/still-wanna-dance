package console

import (
	"encoding/json"
	"errors"
)

const unavailableProxySecret = "已保存的代理凭据无法解密，请重新输入密码并保存、重启；原密文仍保留"

type proxySecretEnvelope struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"`
	Ciphertext []byte `json:"ciphertext"`
}

func loadProxySecret(fields map[string]json.RawMessage, s *Settings) {
	raw := fields["proxySecret"]
	if len(raw) == 0 {
		return
	}
	s.proxySecret = string(raw)
	s.proxySecretError = unavailableProxySecret
	var envelope proxySecretEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Version != 1 || envelope.Protection != "dpapi-current-user" {
		return
	}
	plain, err := unprotectUserData(envelope.Ciphertext)
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
	cipher, err := protectUserData([]byte(s.SOCKS5Password))
	if err != nil {
		return nil, err
	}
	return json.Marshal(proxySecretEnvelope{1, "dpapi-current-user", cipher})
}
