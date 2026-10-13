package main

import (
	"testing"
	"time"
)

func TestCPUPlan(t *testing.T) {
	for _, kind := range []string{"idle", "burst", "stagger", "stepped"} {
		stages, duration, err := cpuSchedule(kind, 32, 6*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		want := 6 * time.Second
		if kind == "stagger" {
			want += 7750 * time.Millisecond
		}
		if kind == "stepped" {
			want = 42 * time.Second
		}
		if duration != want {
			t.Fatalf("%s duration=%s", kind, duration)
		}
		prev := -1
		for _, s := range stages {
			if s.N <= prev || s.N > 32 {
				t.Fatal("invalid increasing concurrency")
			}
			prev = s.N
		}
	}
	for _, n := range []int{-1, 0, 33, 100} {
		if _, _, err := cpuSchedule("burst", n, 6*time.Second); err == nil {
			t.Fatal("unsafe concurrency accepted")
		}
	}
	if _, _, err := cpuSchedule("stepped", 16, 6*time.Second); err == nil {
		t.Fatal("incomplete staircase accepted")
	}
}
