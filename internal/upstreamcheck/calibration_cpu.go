package upstreamcheck

import (
	"runtime"
	"slices"
	"time"
)

// Process user+kernel CPU only, normalized to total logical CPU capacity.
// Percentiles weight each ~100 ms observation by its actual wall duration, so
// scheduling delays and the final short interval do not distort the tails.
type calibrationCPU struct {
	at                time.Time
	used              time.Duration
	valid             bool
	processors        int
	elapsed, consumed time.Duration
	samples           []calibrationCPUWindow
}
type calibrationCPUWindow struct {
	percent  float64
	duration time.Duration
}

func newCalibrationCPU(now time.Time) calibrationCPU {
	used, ok := calibrationCPUTime()
	return calibrationCPU{at: now, used: used, valid: ok, processors: runtime.NumCPU()}
}

func (c *calibrationCPU) observe(now time.Time, used time.Duration, ok bool) {
	c.observeSelected(now, used, ok, true)
}

// Every boundary advances the baseline; only qualified full-load intervals
// contribute to that mean. Warmup, ramp-up and draining are excluded.
func (c *calibrationCPU) observeSelected(now time.Time, used time.Duration, ok, selected bool) {
	if !c.valid {
		return
	}
	if !ok || used < c.used {
		c.valid = false
		return
	}
	duration := now.Sub(c.at)
	if duration <= 0 {
		return
	}
	if !selected {
		c.at, c.used = now, used
		return
	}
	consumed := used - c.used
	percent := float64(consumed) / float64(duration) / float64(max(1, c.processors)) * 100
	c.samples = append(c.samples, calibrationCPUWindow{percent, duration})
	c.elapsed += duration
	c.consumed += consumed
	c.at, c.used = now, used
}

func (c *calibrationCPU) values() (average, p95, p99 *float64) {
	if !c.valid || c.elapsed <= 0 || len(c.samples) == 0 {
		return
	}
	avg := float64(c.consumed) / float64(c.elapsed) / float64(max(1, c.processors)) * 100
	samples := slices.Clone(c.samples)
	slices.SortFunc(samples, func(a, b calibrationCPUWindow) int {
		if a.percent < b.percent {
			return -1
		}
		if a.percent > b.percent {
			return 1
		}
		return 0
	})
	var elapsed time.Duration
	for _, sample := range samples {
		elapsed += sample.duration
		if p95 == nil && float64(elapsed) >= float64(c.elapsed)*0.95 {
			value := sample.percent
			p95 = &value
		}
		if p99 == nil && float64(elapsed) >= float64(c.elapsed)*0.99 {
			value := sample.percent
			p99 = &value
		}
	}
	return &avg, p95, p99
}

func (c *calibrationCPU) average() *float64 {
	if !c.valid || c.elapsed <= 0 {
		return nil
	}
	v := float64(c.consumed) / float64(c.elapsed) / float64(max(1, c.processors)) * 100
	return &v
}
