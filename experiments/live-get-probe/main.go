// live-get-probe is a loopback-only SOCKS5 lab relay. It limits aggregate
// upstream-to-gateway TCP bytes without modifying the production cache engine.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/console"
)

const quantum = 16 * 1024

// Reservations share one clock across all streams. No accumulated idle credit:
// each chunk is delayed until its complete byte allowance has elapsed.
// A canceled reservation may leave a small unused allowance, never excess data.
type pacer struct {
	mu   sync.Mutex
	rate int64
	next time.Time
}

func (p *pacer) wait(ctx context.Context, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.rate == 0 {
		return nil
	}
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	p.next = p.next.Add(time.Duration((int64(n)*int64(time.Second) + p.rate - 1) / p.rate))
	delay := time.Until(p.next)
	p.mu.Unlock()
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type relay struct {
	pace    *pacer
	dial    func(context.Context, string, string) (net.Conn, error)
	log     *slog.Logger
	seq     atomic.Uint64
	active  atomic.Int64
	limited atomic.Int64
	exempt  atomic.Int64
}

func destination(c net.Conn) (string, string, error) {
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return "", "", err
	}
	if head[0] != 5 || head[1] == 0 {
		return "", "", fmt.Errorf("invalid SOCKS greeting")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", "", err
	}
	noAuth := false
	for _, m := range methods {
		noAuth = noAuth || m == 0
	}
	if !noAuth {
		c.Write([]byte{5, 255})
		return "", "", fmt.Errorf("no supported authentication")
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return "", "", err
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", "", err
	}
	if req[0] != 5 || req[1] != 1 || req[2] != 0 {
		return "", "", fmt.Errorf("CONNECT required")
	}
	var host string
	switch req[3] {
	case 1, 4:
		n := 4
		if req[3] == 4 {
			n = 16
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", "", err
		}
		host = net.IP(b).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return "", "", err
		}
		b := make([]byte, int(n[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", "", err
		}
		host = string(b)
	default:
		return "", "", fmt.Errorf("invalid address type")
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return "", "", err
	}
	p := binary.BigEndian.Uint16(port[:])
	if host == "" || p != 443 {
		return "", "", fmt.Errorf("lab relay only permits HTTPS destinations")
	}
	return host, net.JoinHostPort(host, "443"), nil
}

func (r *relay) serve(ctx context.Context, client net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	client.SetDeadline(time.Now().Add(15 * time.Second))
	host, target, err := destination(client)
	if err != nil {
		r.log.Info("handshake_failed", "error", err.Error())
		return
	}
	upstream, err := r.dial(ctx, "tcp", target)
	if err != nil {
		client.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		r.log.Info("dial_failed", "host", host, "error", err.Error())
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stopUpstream()
	if _, err = client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	client.SetDeadline(time.Time{})
	id := r.seq.Add(1)
	r.active.Add(1)
	defer r.active.Add(-1)
	limited := host != "api.udon.dance" && host != "x.kiva.moe"
	r.log.Info("connection_started", "id", id, "host", host, "limited", limited)
	started := time.Now()
	var sent int64
	defer func() {
		r.log.Info("connection_finished", "id", id, "host", host, "limited", limited, "bytes", sent, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		_, _ = io.Copy(upstream, client)
		cancel()
	}()
	defer func() { cancel(); client.Close(); upstream.Close(); <-upDone }()
	buf := make([]byte, quantum)
	for {
		n, readErr := upstream.Read(buf)
		if n > 0 {
			if limited {
				if err := r.pace.wait(ctx, n); err != nil {
					return
				}
			}
			written, writeErr := client.Write(buf[:n])
			sent += int64(written)
			if limited {
				r.limited.Add(int64(written))
			} else {
				r.exempt.Add(int64(written))
			}
			if writeErr != nil || written != n {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

func main() {
	address := flag.String("listen", "127.0.0.1:19081", "loopback SOCKS5 address")
	rate := flag.Int64("bytes-per-second", 0, "aggregate incoming TCP rate; 0 unlimited")
	stopFile := flag.String("stop-file", "", "stop when this file exists")
	duration := flag.Duration("duration", 45*time.Minute, "maximum lab lifetime")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || *rate < 0 || *duration <= 0 {
		panic("invalid lab options")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	listener, err := net.Listen("tcp4", *address)
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	context.AfterFunc(ctx, func() { listener.Close() })
	r := &relay{pace: &pacer{rate: *rate}, dial: console.NewUpstreamDialer(), log: log}
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		next := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if *stopFile != "" {
					if _, err := os.Stat(*stopFile); err == nil {
						cancel()
						return
					}
				}
				if time.Now().After(next) {
					log.Info("sample", "active", r.active.Load(), "limited_bytes", r.limited.Load(), "exempt_bytes", r.exempt.Load())
					next = time.Now().Add(time.Second)
				}
			}
		}
	}()
	log.Info("ready", "listen", *address, "bytes_per_second", *rate, "quantum", quantum, "max_seconds", duration.Seconds())
	var workers sync.WaitGroup
	slots := make(chan struct{}, 64)
	for {
		c, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Error("accept_failed", "error", err.Error())
			}
			break
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func() { defer workers.Done(); defer func() { <-slots }(); r.serve(ctx, c) }()
		default:
			c.Close()
		}
	}
	cancel()
	workers.Wait()
	log.Info("stopped", "active", r.active.Load(), "limited_bytes", r.limited.Load(), "exempt_bytes", r.exempt.Load())
}
