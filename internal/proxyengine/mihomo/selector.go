package mihomo

import (
	"context"
	"sync"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	C "github.com/metacubex/mihomo/constant"
)

// Mihomo v1.19.32's Selector reads/writes selected without synchronization.
// Guard that native state at the adapter boundary, including reads performed by
// native API serialization and health checks. Node choice remains native.
type synchronizedSelector struct {
	*outboundgroup.Selector
	mu sync.RWMutex
}

func (s *synchronizedSelector) Set(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Selector.Set(name)
}

func (s *synchronizedSelector) ForceSet(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Selector.ForceSet(name)
}

func (s *synchronizedSelector) Now() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Selector.Now()
}

func (s *synchronizedSelector) MarshalJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Selector.MarshalJSON()
}

func (s *synchronizedSelector) SupportUDP() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Selector.SupportUDP()
}

func (s *synchronizedSelector) IsL3Protocol(metadata *C.Metadata) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Selector.IsL3Protocol(metadata)
}

func (s *synchronizedSelector) Unwrap(metadata *C.Metadata, touch bool) C.Proxy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Selector.Unwrap(metadata, touch)
}

func (s *synchronizedSelector) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	// Snapshot native selection before dialing so an in-flight handshake does
	// not hold the selection lock. Preserve the native connection chain.
	c, err := s.Unwrap(metadata, true).DialContext(ctx, metadata)
	if err == nil {
		c.AppendToChains(s)
	}
	return c, err
}

func (s *synchronizedSelector) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	c, err := s.Unwrap(metadata, true).ListenPacketContext(ctx, metadata)
	if err == nil {
		c.AppendToChains(s)
	}
	return c, err
}
