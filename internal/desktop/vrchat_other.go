//go:build !windows

package desktop

import "errors"

func findVRChatProcess() (processWaiter, error) {
	return nil, errors.New("随 VRChat 退出仅支持 Windows")
}
