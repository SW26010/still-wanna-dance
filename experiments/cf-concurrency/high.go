package main

import (
	"context"
	"errors"
	"fmt"
)

// Cyclic Latin square: every concurrency occurs in every position once.
// Five rounds do not fully balance all ordered carry-over pairs.
func highOrders(ip string) [][]sweepArm {
	levels := []int{8, 12, 16, 24, 32}
	positions := []int{0, 1, 4, 2, 3}
	orders := make([][]sweepArm, len(levels))
	for round := range levels {
		for _, position := range positions {
			n := levels[(position+round)%len(levels)]
			orders[round] = append(orders[round], sweepArm{fmt.Sprintf("ip1-%d", n), n, []string{ip}})
		}
	}
	return orders
}

func high(ctx context.Context, t target, ips []string, size int64, out string) error {
	if len(ips) != 1 || validateIPs(ips) != nil {
		return errors.New("high-concurrency experiment requires exactly one IPv4 address")
	}
	if size < 96*blockSize || size > 128*blockSize || size%blockSize != 0 || size > t.Size {
		return errors.New("high-concurrency experiment requires a 96..128 MiB interval within the resource")
	}
	return executePlan(ctx, t, ips, size, out, highOrders(ips[0]), "5 complete Latin-square rounds; 8/12/16/24/32 total requests; one pinned IPv4; 1 MiB unique ranges; fresh product candidate pool; at least 96 blocks; strong ETag and SHA256; no retries; not product engine or game")
}
