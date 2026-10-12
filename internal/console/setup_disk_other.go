//go:build !windows

package console

import "errors"

func setupDiskSpace(string) (uint64, uint64, error) {
	return 0, 0, errors.New("当前平台未提供磁盘空间检测")
}
