package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMultiIPPinsIdentityAndSharesOnlyBlockQueue(t *testing.T) {
	body, hashes := fixtureBody()
	ips := []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}
	for _, n := range []int{4, 6, 8, 12} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var mu sync.Mutex
			initial := make(map[string]int)
			seen := make(map[int64]int)
			gate := make(chan struct{})
			arrived := 0
			port := "0"
			for _, ip := range ips {
				listener, err := net.Listen("tcp4", net.JoinHostPort(ip, port))
				if err != nil {
					t.Fatal(err)
				}
				_, port, _ = net.SplitHostPort(listener.Addr().String())
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Host != "experiment.test:"+port || r.Header.Get("If-Match") != `"fixed"` {
						t.Error("original Host or entity condition changed")
					}
					var start, end int64
					if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					seen[start]++
					arrived++
					if arrived <= n {
						initial[ip]++
					}
					if arrived == n {
						close(gate)
					}
					mu.Unlock()
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
					w.Header().Set("ETag", `"fixed"`)
					w.WriteHeader(206)
					w.Write(body[start : end+1])
				}))
				server.Listener.Close()
				server.Listener = listener
				server.Start()
				defer server.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result := measureIPs(ctx, target{URL: "http://experiment.test:" + port, Host: "experiment.test", Size: int64(len(body)), ETag: `"fixed"`}, ips, int64(len(body)), n, hashes, time.Now())
			if !result.OK || result.Peak != n || result.Bytes != int64(len(body)) {
				t.Fatalf("invalid result: %+v", result)
			}
			for i, ip := range ips {
				want := n / len(ips)
				if i < n%len(ips) {
					want++
				}
				if initial[ip] != want {
					t.Fatalf("IP %s got %d initial workers, want %d", ip, initial[ip], want)
				}
			}
			if len(seen) != len(hashes) {
				t.Fatal("missing blocks")
			}
			for _, count := range seen {
				if count != 1 {
					t.Fatal("duplicate block")
				}
			}
			for _, o := range result.Requests {
				peer, _, _ := net.SplitHostPort(o.Remote)
				if peer != o.TargetIP {
					t.Fatal("target and actual peer differ")
				}
			}
		})
	}
}

func TestReferenceRejectsChangedContentAcrossIP(t *testing.T) {
	body, hashes := fixtureBody()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := bytes.Clone(body[:blockSize])
		data[0] ^= 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", blockSize-1, len(body)))
		w.Header().Set("ETag", `"fixed"`)
		w.WriteHeader(206)
		w.Write(data)
	}))
	defer server.Close()
	result := measureIPs(context.Background(), target{URL: server.URL, Host: "127.0.0.1", Size: int64(len(body)), ETag: `"fixed"`}, []string{"127.0.0.1"}, blockSize, 1, hashes, time.Now())
	if result.OK || result.Error != "block SHA256 mismatch" {
		t.Fatalf("changed body accepted: %+v", result)
	}
}

func TestSweepPlanCompleteReproducibleAndBounded(t *testing.T) {
	ips := []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}
	orders := sweepOrders(ips)
	if !reflect.DeepEqual(orders, sweepOrders(ips)) || len(orders) != 3 {
		t.Fatal("unstable plan")
	}
	for round, arms := range orders {
		seen := make(map[string]bool)
		if len(arms) != 17 {
			t.Fatal("incomplete factorial")
		}
		for _, arm := range arms {
			if seen[arm.Name] || arm.N > 12 || validateIPs(arm.IPs) != nil {
				t.Fatal("duplicate or invalid arm")
			}
			seen[arm.Name] = true
			if len(arm.IPs) == 3 && arm.IPs[0] != ips[round] {
				t.Fatal("surplus worker assignment not rotated")
			}
		}
	}
	for _, bad := range [][]string{nil, {""}, {"host"}, {"127.0.0.1", "127.0.0.1"}, {"::1"}, {"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"}} {
		if validateIPs(bad) == nil {
			t.Fatal("invalid IP set accepted")
		}
	}
}
