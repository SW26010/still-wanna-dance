//go:build !windows

package upstreamstate

import "time"

func calibrationCPUTime() (time.Duration, bool) { return 0, false }
