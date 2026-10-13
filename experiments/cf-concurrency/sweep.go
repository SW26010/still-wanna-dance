package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"time"
)

func validateIPs(ips []string) error {
	if len(ips) < 1 || len(ips) > 3 {
		return errors.New("expected 1..3 unique IPv4 addresses")
	}
	seen := make(map[string]bool)
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil || seen[parsed.String()] {
			return errors.New("invalid or duplicate IPv4 address")
		}
		seen[parsed.String()] = true
	}
	return nil
}

type sweepArm struct {
	Name string   `json:"name"`
	N    int      `json:"concurrency"`
	IPs  []string `json:"ips"`
}

// Every round contains every arm once. Shuffling is fixed before observations;
// the surplus worker allocation rotates across IPs in the three rounds.
func sweepOrders(ips []string) [][]sweepArm {
	rng := rand.New(rand.NewSource(20261014))
	var orders [][]sweepArm
	for round := range 3 {
		var arms []sweepArm
		for i, ip := range ips {
			for _, n := range []int{4, 6, 8, 12} {
				arms = append(arms, sweepArm{fmt.Sprintf("ip%d-%d", i+1, n), n, []string{ip}})
			}
		}
		if len(ips) > 1 {
			rotated := append(append([]string(nil), ips[round%len(ips):]...), ips[:round%len(ips)]...)
			for _, n := range []int{4, 6, 8, 12} {
				arms = append(arms, sweepArm{fmt.Sprintf("multi-%d", n), n, rotated})
			}
		}
		arms = append(arms, sweepArm{"long-ip1", 0, ips[:1]})
		rng.Shuffle(len(arms), func(i, j int) { arms[i], arms[j] = arms[j], arms[i] })
		orders = append(orders, arms)
	}
	return orders
}

func sweep(ctx context.Context, t target, ips []string, size int64, out string) error {
	if err := validateIPs(ips); err != nil {
		return err
	}
	return executePlan(ctx, t, ips, size, out, sweepOrders(ips), "3 complete randomized blocks; seed 20261014; fixed per-IP worker slots with a shared block queue; rotated surplus slots; fresh product candidate pools; strong ETag and per-block SHA256; supplied DNS IPv4 addresses; 12 requests max; not product engine or game")
}

func executePlan(ctx context.Context, t target, ips []string, size int64, out string, orders [][]sweepArm, method string) error {
	if err := validateIPs(ips); err != nil {
		return err
	}
	if t.ETag == "" || len(t.ETag) < 2 || t.ETag[:2] == "W/" {
		return errors.New("sweep requires a strong ETag from preflight")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	zero := time.Now()
	report := struct {
		Host     string       `json:"host"`
		IPs      []string     `json:"ips"`
		Song     int          `json:"song"`
		Size     int64        `json:"resourceBytes"`
		Interval int64        `json:"intervalBytes"`
		Method   string       `json:"method"`
		Orders   [][]sweepArm `json:"orders"`
		Runs     []runResult  `json:"runs"`
	}{Host: t.Host, IPs: ips, Song: t.Song, Size: t.Size, Interval: size,
		Method: method,
		Orders: orders}
	var reference []string
	execute := func(round int, arm sweepArm) (runResult, error) {
		trial, stop := context.WithTimeout(ctx, 120*time.Second)
		defer stop()
		r := measureIPs(trial, t, arm.IPs, size, arm.N, reference, zero)
		r.Round, r.Arm = round, arm.Name
		report.Runs = append(report.Runs, r)
		fmt.Printf("round=%d arm=%s ok=%v seconds=%.3f MiB/s=%.3f peak=%d mean_active=%.2f bytes=%d\n", round, r.Arm, r.OK, r.ElapsedMS/1000, r.MiBPerSec, r.Peak, r.MeanActive, r.Bytes)
		return r, save(out, report)
	}
	// Each IP gets a complete warmup, verified against the first IP's hashes.
	// A failed IP is recorded and stops the batch instead of silently dropping it.
	for i, ip := range ips {
		r, err := execute(0, sweepArm{fmt.Sprintf("warm-ip%d", i+1), 0, []string{ip}})
		if err != nil {
			return err
		}
		if !r.OK {
			return errors.New("cross-IP warmup failed; sweep stopped")
		}
		if reference == nil {
			reference = r.Requests[0].Hashes
		}
	}
	failures := 0
	for round, order := range report.Orders {
		for _, arm := range order {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			r, err := execute(round+1, arm)
			if err != nil {
				return err
			}
			if r.OK {
				failures = 0
			} else {
				failures++
			}
			for _, o := range r.Requests {
				if o.Status == 429 || o.Status == 503 {
					return errors.New("upstream throttling/service-unavailable response; stopped")
				}
			}
			if failures >= 2 {
				return errors.New("two consecutive failed arms; stopped")
			}
		}
	}
	return nil
}
