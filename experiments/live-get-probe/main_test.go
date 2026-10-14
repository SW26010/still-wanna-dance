package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

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
