package cacheproxy

import (
	"errors"
	"golang.org/x/sys/windows"
)

func importCrossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }
