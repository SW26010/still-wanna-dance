//go:build !windows

package desktop

import (
	"context"
	"errors"
)

func SelectPath(context.Context, PathDialog) (string, error) {
	return "", errors.New("路径选择器仅支持 Windows")
}
