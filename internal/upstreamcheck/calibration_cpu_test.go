package upstreamcheck

import (
	"testing"
	"time"
)

func TestCalibrationCPUPercentilesAndMean(t *testing.T) {
	at := time.Unix(100, 0)
	c := calibrationCPU{at: at, valid: true, processors: 2}
	var used time.Duration
	for i := range 100 {
		used += time.Duration(i) * 2 * time.Millisecond
		c.observe(at.Add(time.Duration(i+1)*100*time.Millisecond), used, true)
	}
	avg, p95, p99 := c.values()
	if avg == nil || *avg != 49.5 || p95 == nil || *p95 != 94 || p99 == nil || *p99 != 98 {
		t.Fatalf("CPU values: %v %v %v", avg, p95, p99)
	}
	*p99 = 0
	_, _, again := c.values()
	if *again != 98 {
		t.Fatal("result aliases samples")
	}
}

func TestCalibrationCPUWeightsDelayedAndFinalIntervals(t *testing.T) {
	at := time.Unix(100, 0)
	c := calibrationCPU{at: at, valid: true, processors: 1}
	c.observe(at.Add(10*time.Second), time.Second, true)                                   // 10% for 10 s
	c.observe(at.Add(10*time.Second+time.Millisecond), time.Second+time.Millisecond, true) // 100% for 1 ms
	avg, p95, p99 := c.values()
	if avg == nil || *avg <= 10 || *avg >= 10.01 || p95 == nil || *p95 != 10 || p99 == nil || *p99 != 10 {
		t.Fatal("short tail distorted CPU distribution")
	}
	c.observe(at.Add(11*time.Second), 0, false)
	if avg, p95, p99 := c.values(); avg != nil || p95 != nil || p99 != nil {
		t.Fatal("failed counter became zero usage")
	}
}

func TestCalibrationCPUUnavailableAndCounterReset(t *testing.T) {
	var c calibrationCPU
	if avg, p95, p99 := c.values(); avg != nil || p95 != nil || p99 != nil {
		t.Fatal("missing data reported usage")
	}
	c = calibrationCPU{at: time.Unix(100, 0), used: time.Second, valid: true, processors: 1}
	c.observe(c.at.Add(time.Second), 0, true)
	if avg, _, _ := c.values(); avg != nil {
		t.Fatal("counter reset produced a negative sample")
	}
}

func TestCalibrationFullCPUExcludesPreparationRampAndDrain(t *testing.T) {
	at := time.Unix(100, 0)
	cpu := calibrationCPU{at: at, valid: true, processors: 2}
	cpu.observeSelected(at.Add(time.Second), 2*time.Second, true, false)
	if cpu.average() != nil {
		t.Fatal("warmup became full CPU")
	}
	cpu.observeSelected(at.Add(2*time.Second), 2200*time.Millisecond, true, true)
	cpu.observeSelected(at.Add(3*time.Second), 4200*time.Millisecond, true, false)
	cpu.observeSelected(at.Add(4*time.Second), 4400*time.Millisecond, true, true)
	cpu.observeSelected(at.Add(5*time.Second), 6400*time.Millisecond, true, false)
	if got := cpu.average(); got == nil || *got != 10 {
		t.Fatalf("full average contaminated: %v", got)
	}
	cpu.observeSelected(at.Add(6*time.Second), 0, false, false)
	if cpu.average() != nil {
		t.Fatal("failed counter became zero CPU")
	}
}
