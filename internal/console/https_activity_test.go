package console

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPSVideoActivityExcludesAPITunnel(t *testing.T) {
	for _, host := range []string{"play.udon.dance", "nya.xin.moe", "api.udon.dance"} {
		t.Run(host, func(t *testing.T) {
			var active, begins atomic.Int32
			p := &httpsRelay{ctx: context.Background(), beginResourceLoad: func() func() {
				active.Add(1)
				begins.Add(1)
				return func() { active.Add(-1) }
			}}
			p.dial = func(context.Context, string, string) (net.Conn, error) {
				want := int32(1)
				if host == "api.udon.dance" {
					want = 0
				}
				if active.Load() != want {
					t.Error("wrong video activity scope")
				}
				return nil, errors.New("test dial failure")
			}
			server, client := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() { defer close(done); defer server.Close(); _ = p.forward(server) }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = tls.Client(client, &tls.Config{ServerName: host}).HandshakeContext(ctx)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("forward did not finish")
			}
			if active.Load() != 0 {
				t.Fatal("failed tunnel retained activity")
			}
			if host != "api.udon.dance" && begins.Load() != 1 {
				t.Fatal("video tunnel not covered")
			}
		})
	}
}
