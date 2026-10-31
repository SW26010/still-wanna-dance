package mihomo

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/transport/anytls/padding"
	"github.com/metacubex/mihomo/transport/anytls/session"
)

// A peer/session shutdown may race the caller's read, write, or local close.
// Keep this at the native Stream boundary: wrapping the returned net.Conn does
// not synchronize Mihomo's receive goroutine with its caller.
func TestAnyTLSStreamConcurrentClose(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		for range 32 {
			local, peer := net.Pipe()
			peerDone := make(chan struct{})
			go func() { defer close(peerDone); _, _ = io.Copy(io.Discard, peer) }()
			var scheme atomic.Pointer[padding.PaddingFactory]
			scheme.Store(padding.NewPaddingFactory(padding.DefaultPaddingScheme))
			sess := session.NewClientSession(local, &scheme, "")
			stream, err := sess.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var work sync.WaitGroup
			for _, operation := range []func(){
				func() { _, _ = stream.Read(make([]byte, 1)) },
				func() { _, _ = stream.Write([]byte("payload")) },
				func() {
					if closeSession {
						_ = sess.Close()
					} else {
						_ = stream.Close()
					}
				},
			} {
				work.Go(func() { <-start; operation() })
			}
			close(start)
			work.Wait()
			if _, err := stream.Write([]byte("after close")); err == nil {
				t.Fatal("closed stream accepted a write")
			}
			_ = sess.Close()
			_ = peer.Close()
			<-peerDone
		}
	}
}
