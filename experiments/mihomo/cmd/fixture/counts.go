package main

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

type countingListen struct {
	net.ListenConfig
	tcp      atomic.Int64
	mu       sync.Mutex
	udpPeers map[string]bool
}
type countingListener struct {
	net.Listener
	counter *countingListen
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e == nil {
		l.counter.tcp.Add(1)
	}
	return c, e
}
func (l *countingListen) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	n, e := l.ListenConfig.Listen(ctx, network, address)
	if e != nil {
		return nil, e
	}
	return &countingListener{n, l}, nil
}

type countingPacket struct {
	net.PacketConn
	counter *countingListen
}

func (p *countingPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, e := p.PacketConn.ReadFrom(b)
	if e == nil {
		p.counter.mu.Lock()
		p.counter.udpPeers[a.String()] = true
		p.counter.mu.Unlock()
	}
	return n, a, e
}
func (l *countingListen) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	p, e := l.ListenConfig.ListenPacket(ctx, network, address)
	if e != nil {
		return nil, e
	}
	return &countingPacket{p, l}, nil
}
func (l *countingListen) count() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tcp.Load() + int64(len(l.udpPeers))
}
