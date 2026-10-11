//go:build !windows

package console

import "errors"

func protectUserData([]byte) ([]byte, error) {
	return nil, errors.New("凭据持久化需要 Windows CurrentUser 保护")
}
func unprotectUserData([]byte) ([]byte, error) {
	return nil, errors.New("凭据解密需要 Windows CurrentUser 保护")
}
