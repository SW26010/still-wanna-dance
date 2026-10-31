package upstreamcheck

import (
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// Observation only. The owner changes the ceiling after "ramping"; this
// accumulator never submits work or controls the natural growth cadence.
type calibrationSpeeds struct {
	at                     time.Time
	bytes                  int64
	open, active           int
	stage                  string
	warmup, window, warmed time.Duration
	firstByte              bool
	single, full           calibrationSpeed
}

func newCalibrationSpeeds(warmup, window time.Duration) calibrationSpeeds {
	return calibrationSpeeds{stage: "single_warmup", warmup: warmup, window: window}
}

type calibrationSpeed struct {
	bytes    int64
	duration time.Duration
}

func (s *calibrationSpeeds) observe(now time.Time, received int64, open, active int) {
	defer func() { s.at, s.bytes, s.open, s.active = now, received, open, active }()
	if !s.firstByte {
		// Headers/first-byte waiting is not a single-lane throughput sample.
		s.firstByte = received > 0
		return
	}
	if s.at.IsZero() || !now.After(s.at) || received < s.bytes {
		return
	}
	full := s.open == cacheproxy.VideoRequestSoftLimit && s.active == cacheproxy.VideoRequestSoftLimit
	single := s.open == 1 && s.active == 1
	duration, bytes := now.Sub(s.at), received-s.bytes
	switch s.stage {
	case "single_warmup", "full_warmup":
		if s.stage == "single_warmup" && !single || s.stage == "full_warmup" && !full {
			return
		}
		s.warmed += duration
		if s.warmed >= s.warmup {
			if s.stage == "single_warmup" {
				s.stage = "single"
			} else {
				s.stage = "full"
			}
		}
	case "single":
		if single {
			s.single.bytes += bytes
			s.single.duration += duration
			if s.single.duration >= s.window {
				s.stage = "ramping"
			}
		}
	case "ramping":
		if open == cacheproxy.VideoRequestSoftLimit && active == cacheproxy.VideoRequestSoftLimit {
			s.stage, s.warmed = "full_warmup", 0
		}
	case "full":
		if full {
			s.full.bytes += bytes
			s.full.duration += duration
			if s.full.duration >= s.window {
				s.stage = "complete"
			}
		}
	}
}

func (s *calibrationSpeeds) values() (single, full *float64) {
	if s.single.duration >= s.window && s.single.duration > 0 {
		v := float64(s.single.bytes) / s.single.duration.Seconds()
		single = &v
	}
	if s.full.duration >= s.window && s.full.duration > 0 {
		v := float64(s.full.bytes) / s.full.duration.Seconds()
		full = &v
	}
	return
}

// Provisional means use only qualified intervals too. Keep them separate from
// the final values so an interrupted short measurement cannot become advice.
func (s *calibrationSpeeds) liveValues() (single, full *float64) {
	if s.single.duration >= 250*time.Millisecond {
		v := float64(s.single.bytes) / s.single.duration.Seconds()
		single = &v
	}
	if s.full.duration >= 250*time.Millisecond {
		v := float64(s.full.bytes) / s.full.duration.Seconds()
		full = &v
	}
	return
}

// Rolling throughput is visual feedback throughout loading, including warmup
// and growth. It never contributes to the qualified single/full results.
type calibrationLiveRate struct {
	points []calibrationRatePoint
	value  *float64
}

type calibrationRatePoint struct {
	at    time.Time
	bytes int64
}

func (r *calibrationLiveRate) observe(now time.Time, received int64) *float64 {
	if len(r.points) > 0 && now.Sub(r.points[len(r.points)-1].at) < 100*time.Millisecond {
		return r.value
	}
	r.points = append(r.points, calibrationRatePoint{now, received})
	cutoff := now.Add(-time.Second)
	for len(r.points) > 2 && !r.points[1].at.After(cutoff) {
		r.points = r.points[1:]
	}
	first := r.points[0]
	if elapsed := now.Sub(first.at); elapsed >= 250*time.Millisecond {
		value := float64(max(0, received-first.bytes)) / elapsed.Seconds()
		r.value = &value
	}
	return r.value
}
