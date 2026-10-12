//go:build !windows

package desktop

import (
	"context"
	"errors"
)

func SelectFolder(context.Context, string, string) (string, error) {
	return "", errors.New("文件夹选择器仅支持 Windows")
}
