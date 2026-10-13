package main

import (
	"context"
	"testing"
	"time"
)

func TestHighPlanBalancesAllPositions(t *testing.T) {
	orders := highOrders("127.0.0.1")
	if len(orders) != 5 {
		t.Fatal("expected five rounds")
	}
	positions := make([]map[int]bool, 5)
	for i := range positions {
		positions[i] = make(map[int]bool)
	}
	for _, row := range orders {
		seen := make(map[int]bool)
		if len(row) != 5 {
			t.Fatal("incomplete round")
		}
		for i, arm := range row {
			if seen[arm.N] || positions[i][arm.N] {
				t.Fatal("unbalanced plan")
			}
			seen[arm.N], positions[i][arm.N] = true, true
			if len(arm.IPs) != 1 || arm.IPs[0] != "127.0.0.1" || (arm.N != 8 && arm.N != 12 && arm.N != 16 && arm.N != 24 && arm.N != 32) {
				t.Fatal("unexpected arm")
			}
		}
	}
}

func TestHighRejectsUndersizedOrMultiIPExperiments(t *testing.T) {
	for _, size := range []int64{30 * blockSize, 95 * blockSize, 129 * blockSize, 96*blockSize + 1} {
		if high(context.Background(), target{Size: 256 * blockSize}, []string{"127.0.0.1"}, size, "unused.json") == nil {
			t.Fatal("invalid interval accepted")
		}
	}
	if high(context.Background(), target{Size: 128 * blockSize}, []string{"127.0.0.1", "127.0.0.2"}, 96*blockSize, "unused.json") == nil {
		t.Fatal("multiple IPs accepted")
	}
	for _, n := range []int{32, 33} {
		r := measureIPs(context.Background(), target{}, []string{"127.0.0.1"}, 30*blockSize, n, nil, time.Now())
		if r.OK || r.Error == "" || len(r.Requests) != 0 {
			t.Fatal("impossible or unbounded concurrency accepted")
		}
	}
}
