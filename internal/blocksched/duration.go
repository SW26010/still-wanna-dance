package blocksched

import (
	"math"
	"time"
)

const standardBlockSize = 1 << 20
const initialBlockTime = time.Second
const sampleHalfLife = 30 * time.Second

// A time-decayed mean of comparable completed executions. No periodic tick is
// needed: old weight decays only when a new completion contributes a sample.
type durationEstimate struct {
	total, weight float64
	at            time.Time
}

func (d *durationEstimate) value() time.Duration {
	if d.weight == 0 {
		return initialBlockTime
	}
	return time.Duration(d.total / d.weight)
}

func (d *durationEstimate) observe(p *Progress, now time.Time) bool {
	// Tail and resumed fragments pay connection overhead for less work. Do not
	// scale their times up to 1 MiB or let them bias the full-block estimate.
	if p.work != standardBlockSize || p.admitted.IsZero() || !p.ready.After(p.admitted) {
		return false
	}
	if !d.at.IsZero() {
		decay := math.Exp2(-now.Sub(d.at).Seconds() / sampleHalfLife.Seconds())
		d.total *= decay
		d.weight *= decay
	}
	d.total += float64(p.ready.Sub(p.admitted))
	d.weight++
	d.at = now
	return true
}
