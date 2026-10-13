package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestAdmissionLateResultKeepsCapacityUntilUpstreamEnds(t *testing.T) {
	a := newAdmission(1, time.Second)
	defer a.retire()
	entered, release := make(chan struct{}), make(chan struct{})
	client, peer := net.Pipe()
	defer peer.Close()
	dial := a.wrap(func(context.Context, string, string) (net.Conn, error) { close(entered); <-release; return client, nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		c, err := dial(ctx, "tcp", "example.invalid:80")
		if c != nil {
			c.Close()
		}
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil || err.Error() != "dial-capacity" {
		t.Fatalf("must reject without another worker: %v", err)
	}
	close(release)
	if !a.wait(time.Second) {
		t.Fatal("worker did not drain")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("late connection survived")
	}
	s := a.snapshot()
	if s["occupied"] != 0 || s["pending"] != 0 || s["lateClosed"] != 1 {
		t.Fatal(s)
	}
}

func TestAdmissionRetirementOwnsConnectionsAndPendingDials(t *testing.T) {
	a := newAdmission(2, time.Second)
	client, peer := net.Pipe()
	defer peer.Close()
	c, err := a.wrap(func(context.Context, string, string) (net.Conn, error) { return client, nil })(context.Background(), "tcp", "example.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	dial := a.wrap(func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go func() { _, err := dial(context.Background(), "tcp", "example.invalid:80"); done <- err }()
	<-entered
	a.retire()
	if err := <-done; err == nil {
		t.Fatal("pending dial survived retirement")
	}
	if !a.wait(time.Second) {
		t.Fatal("pending dial did not finish")
	}
	if _, err := dial(context.Background(), "tcp", "example.invalid:80"); !errors.Is(err, upstreamrequest.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("x")); err == nil {
		t.Fatal("active connection survived")
	}
	if s := a.snapshot(); s["occupied"] != 0 || s["openConnections"] != 0 {
		t.Fatal(s)
	}
}

func TestAdmissionCancellationHandoffRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		a := newAdmission(1, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		client, peer := net.Pipe()
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); cancel() }()
		c, err := a.wrap(func(context.Context, string, string) (net.Conn, error) { return client, nil })(ctx, "tcp", "example.invalid:80")
		if err == nil {
			c.Close()
		}
		wg.Wait()
		a.retire()
		if !a.wait(time.Second) {
			t.Fatal("race leaked worker")
		}
		client.Close()
		peer.Close()
		if s := a.snapshot(); s["occupied"] != 0 {
			t.Fatal(s)
		}
	}
}

func TestAdmissionPreCancelAndInvalidTargetsNeverDial(t *testing.T) {
	a := newAdmission(1, time.Second)
	defer a.retire()
	dial := a.wrap(func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("unexpected upstream call")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dial(ctx, "tcp", "example.invalid:80"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, addr := range []string{"missing-port", ":80", "example.invalid:0", "example.invalid:65536"} {
		if _, err := dial(context.Background(), "tcp", addr); err == nil {
			t.Fatal(addr)
		}
	}
	if _, err := dial(context.Background(), "udp", "example.invalid:80"); err == nil {
		t.Fatal("UDP accepted")
	}
}
