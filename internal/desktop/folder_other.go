//go:build !windows

package desktop

import (
	"context"
	"errors"
)

func SelectPath(_ context.Context, dialog PathDialog) (string, error) {
	return showPathDialog(dialog)
}

func showPathDialog(PathDialog) (string, error) {
	return "", errors.New("路径选择器仅支持 Windows")
}
