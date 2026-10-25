//go:build !windows

package desktop

import (
	"context"
	"errors"
)

func SelectDatabase(context.Context, string, string) (string, error) {
	return "", errors.New("数据库选择器仅支持 Windows，请填写完整路径")
}
