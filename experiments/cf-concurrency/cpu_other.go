//go:build !windows

package main

import (
	"context"
	"errors"
	"time"
)

func cpuTrial(context.Context, target, string, int, time.Duration, string, string, string) error {
	return errors.New("CPU instrumentation requires Windows")
}
