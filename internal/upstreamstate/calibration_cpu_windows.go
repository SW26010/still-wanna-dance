//go:build windows

package upstreamstate

import (
	"time"

	"golang.org/x/sys/windows"
)

func calibrationCPUTime() (time.Duration, bool) {
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user) != nil {
		return 0, false
	}
	ticks := (uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)) + (uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime))
	return time.Duration(ticks) * 100 * time.Nanosecond, true
}
