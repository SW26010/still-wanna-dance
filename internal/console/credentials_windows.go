package console

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// No LOCAL_MACHINE flag and no path entropy: CurrentUser protection survives
// moving the portable directory. DPAPI manages the user key; no UAC is needed.
func protectUserData(plain []byte) ([]byte, error)    { return userCrypt(plain, true) }
func unprotectUserData(cipher []byte) ([]byte, error) { return userCrypt(cipher, false) }

func userCrypt(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("empty credential")
	}
	in := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, errors.New("Windows 当前用户凭据保护不可用")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	data := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), data...)
	clear(data)
	return result, nil
}
