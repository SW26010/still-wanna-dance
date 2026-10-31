package upstreamcheck

import (
	"math"
	"testing"
	"time"
)

func TestCalibrationLiveRateSmoothsGrowthAndForgetsOldSamples(t *testing.T) {
	var rate calibrationLiveRate
	at := time.Unix(100, 0)
	if rate.observe(at, 0) != nil || rate.observe(at.Add(100*time.Millisecond), 10_000) != nil {
		t.Fatal("instantaneous startup reported as smoothed speed")
	}
	for i := 2; i <= 20; i++ {
		value := rate.observe(at.Add(time.Duration(i)*100*time.Millisecond), int64(i)*10_000)
		if i >= 3 && (value == nil || math.Abs(*value-100_000) > .01) {
			t.Fatalf("steady speed at %d: %v", i, value)
		}
	}
	// A 100 ms burst is spread over the rolling second, not rendered as 10x.
	if value := rate.observe(at.Add(2100*time.Millisecond), 300_000); value == nil || math.Abs(*value-190_000) > .01 {
		t.Fatalf("burst was not smoothed: %v", value)
	}
	for i := 22; i <= 40; i++ {
		rate.observe(at.Add(time.Duration(i)*100*time.Millisecond), 300_000)
	}
	if rate.value == nil || *rate.value != 0 || len(rate.points) > 12 {
		t.Fatalf("stale speed or unbounded history: %+v", rate)
	}
}

func TestCalibrationSpeedsExcludeWarmupRampIdleAndTail(t *testing.T) {
	s := newCalibrationSpeeds(time.Second, 3*time.Second)
	at := time.Unix(100, 0)
	step := func(ms, bytes int64, open, active int) {
		s.observe(at.Add(time.Duration(ms)*time.Millisecond), bytes, open, active)
	}
	step(0, 0, 1, 1)
	step(2000, 0, 1, 1) // Slow headers do not consume the warmup.
	step(2100, 100, 1, 1)
	step(3100, 9_000_000, 1, 1) // Discard even a very fast warmup.
	step(5100, 11_000_000, 1, 1)
	if single, _ := s.values(); single != nil {
		t.Fatal("short single sample accepted")
	}
	if single, full := s.liveValues(); single == nil || *single != 1_000_000 || full != nil {
		t.Fatal("provisional single mean missing or includes warmup")
	}
	step(6100, 12_000_000, 1, 1)
	if s.stage != "ramping" {
		t.Fatal(s.stage)
	}
	step(6200, 13_000_000, 2, 2)
	step(10000, 50_000_000, 16, 15)
	step(10100, 51_000_000, 16, 16)
	step(11100, 60_000_000, 16, 16) // Full warmup discarded too.
	step(13100, 61_000_000, 16, 15)
	step(15100, 70_000_000, 16, 16) // Non-full interval excluded.
	if _, full := s.values(); full != nil {
		t.Fatal("short full sample accepted")
	}
	if _, full := s.liveValues(); full == nil || *full != 500_000 {
		t.Fatal("provisional full mean missing or includes ramp")
	}
	step(16100, 70_500_000, 16, 4)
	step(17100, 99_000_000, 1, 1)
	step(20100, 110_000_000, 1, 0) // Neither tail nor later single activity changes the result.
	single, full := s.values()
	if single == nil || *single != 1_000_000 || full == nil || *full != 500_000 || s.stage != "complete" {
		t.Fatalf("mixed phases polluted speed: single=%v full=%v stage=%s", single, full, s.stage)
	}
	*full = 1
	_, fresh := s.values()
	if *fresh != 500_000 {
		t.Fatal("returned value aliases accumulator")
	}
}

func TestCalibrationSpeedsStartupIsNotSingleSpeed(t *testing.T) {
	s := newCalibrationSpeeds(time.Second, 3*time.Second)
	at := time.Unix(100, 0)
	s.observe(at, 0, 1, 1)
	s.observe(at.Add(250*time.Millisecond), 41_000, 2, 2)
	s.observe(at.Add(4*time.Second), 20_000_000, 16, 16)
	if single, full := s.values(); single != nil || full != nil {
		t.Fatal("startup became a measurement")
	}
}

func TestCalibrationSpeedsStallAfterWarmupIsZero(t *testing.T) {
	s := newCalibrationSpeeds(time.Second, 3*time.Second)
	at := time.Unix(100, 0)
	s.observe(at, 1, 1, 1)
	s.observe(at.Add(time.Second), 10, 1, 1)
	s.observe(at.Add(4*time.Second), 40, 1, 1)
	s.observe(at.Add(8*time.Second), 100, 16, 16)
	s.observe(at.Add(9*time.Second), 200, 16, 16)
	s.observe(at.Add(12*time.Second), 200, 16, 16)
	single, full := s.values()
	if single == nil || *single != 10 || full == nil || *full != 0 {
		t.Fatalf("stall: %v %v", single, full)
	}
}
