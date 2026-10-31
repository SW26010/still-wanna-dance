//go:build !windows

package upstreamcheck

import "time"

func calibrationCPUTime() (time.Duration, bool) { return 0, false }
