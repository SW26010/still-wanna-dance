//go:build !windows

package console

import "errors"

func protectProxySecret([]byte) ([]byte, error) {
	return nil, errors.New("代理凭据持久化需要 Windows CurrentUser 保护")
}
func unprotectProxySecret([]byte) ([]byte, error) {
	return nil, errors.New("代理凭据解密需要 Windows CurrentUser 保护")
}
