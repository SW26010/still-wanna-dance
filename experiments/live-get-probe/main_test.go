package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

func TestDestinationAllowsHTTPAndHTTPS(t *testing.T) {
	for _, tc := range []struct {
		host    string
		port    uint16
		allowed bool
	}{
		{"api.udon.dance", 80, true},
		{"api.udon.dance", 443, true},
		{"video.example", 443, true},
		{"video.example", 80, true},
		{"api.udon.dance", 22, false},
	} {
		t.Run(net.JoinHostPort(tc.host, fmt.Sprint(tc.port)), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			server.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan struct{})
			go func() {
				defer close(done)
				host, address, err := destination(server)
				if (err == nil) != tc.allowed {
					t.Errorf("allowed=%v, error=%v", tc.allowed, err)
				}
				if tc.allowed && (host != tc.host || address != net.JoinHostPort(tc.host, fmt.Sprint(tc.port))) {
					t.Errorf("unexpected destination %q %q", host, address)
				}
			}()
			client.Write([]byte{5, 1, 0})
			if _, err := io.ReadFull(client, make([]byte, 2)); err != nil {
				t.Fatal(err)
			}
			request := append([]byte{5, 1, 0, 3, byte(len(tc.host))}, []byte(tc.host)...)
			request = append(request, byte(tc.port>>8), byte(tc.port))
			if _, err := client.Write(request); err != nil {
				t.Fatal(err)
			}
			<-done
		})
	}
}

func TestAggregatePacingAndCancellation(t *testing.T) {
	p := &pacer{rate: 512 * 1024}
	start := time.Now()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				if err := p.wait(context.Background(), quantum); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	d := time.Since(start)
	if d < time.Second || d > 3*time.Second {
		t.Fatalf("512 KiB across 4 streams took %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p.wait(ctx, quantum) != context.Canceled {
		t.Fatal("cancellation not observed")
	}
	if (&pacer{}).wait(ctx, quantum) != context.Canceled {
		t.Fatal("unlimited cancellation not observed")
	}
}

func TestConnectionPacingDoesNotShareBandwidth(t *testing.T) {
	r := &relay{pace: &pacer{rate: 128 * 1024}, perConnection: true}
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 3 {
		pace := r.connectionPacer()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				if err := pace.wait(ctx, quantum); err != nil {
					t.Errorf("independent stream lost its own allowance: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestRelayPreservesBytesAndClosesOnCancel(t *testing.T) {
	client, server := net.Pipe()
	remote, upstream := net.Pipe()
	defer client.Close()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &relay{pace: &pacer{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), dial: func(context.Context, string, string) (net.Conn, error) { return upstream, nil }}
	done := make(chan struct{})
	go func() { r.serve(ctx, server); close(done) }()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	client.Write([]byte{5, 1, 0})
	b := make([]byte, 2)
	if _, err := io.ReadFull(client, b); err != nil || b[1] != 0 {
		t.Fatalf("greeting: %v %v", b, err)
	}
	host := "video.example"
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	request = append(request, 1, 187)
	client.Write(request)
	if _, err := io.ReadFull(client, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	go func() { remote.Write([]byte("exact bytes")) }()
	payload := make([]byte, 11)
	if _, err := io.ReadFull(client, payload); err != nil || string(payload) != "exact bytes" {
		t.Fatalf("payload: %q %v", payload, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop")
	}
	if r.active.Load() != 0 || r.limited.Load() != 11 {
		t.Fatalf("counters %d %d", r.active.Load(), r.limited.Load())
	}
}
