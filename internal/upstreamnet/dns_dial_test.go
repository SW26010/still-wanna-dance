package upstreamnet

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.getoutline.org/sdk/transport"
)

type dnsTestStream struct {
	net.Conn
	closed atomic.Bool
}

func (c *dnsTestStream) Close() error      { c.closed.Store(true); return nil }
func (c *dnsTestStream) CloseRead() error  { return nil }
func (c *dnsTestStream) CloseWrite() error { return nil }

func TestDNSHappyEyeballs(t *testing.T) {
	for _, tc := range []struct {
		name, network, address string
		ips                    []string
		v6                     string
		want                   []string
		delay                  time.Duration
	}{
		{"IPv6 preferred", "tcp", "example.invalid:443", []string{"192.0.2.1", "2001:db8::1"}, "works", []string{"[2001:db8::1]:443"}, 0},
		{"IPv6 refused", "tcp", "example.invalid:443", []string{"192.0.2.1", "2001:db8::1"}, "refused", []string{"[2001:db8::1]:443", "192.0.2.1:443"}, 0},
		{"IPv6 stalled", "tcp", "example.invalid:443", []string{"192.0.2.1", "2001:db8::1"}, "stalled", []string{"[2001:db8::1]:443", "192.0.2.1:443"}, 250 * time.Millisecond},
		{"IPv4 only", "tcp", "example.invalid:443", []string{"192.0.2.1"}, "works", []string{"192.0.2.1:443"}, 0},
		{"IPv6 only", "tcp", "example.invalid:443", []string{"2001:db8::1"}, "works", []string{"[2001:db8::1]:443"}, 0},
		{"explicit tcp4", "tcp4", "example.invalid:443", []string{"2001:db8::1", "192.0.2.1"}, "works", []string{"192.0.2.1:443"}, 0},
		{"explicit tcp6", "tcp6", "example.invalid:443", []string{"192.0.2.1", "2001:db8::1"}, "works", []string{"[2001:db8::1]:443"}, 0},
		{"IPv6 literal", "tcp", "[2001:db8::1]:443", []string{"2001:db8::1"}, "works", []string{"[2001:db8::1]:443"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var attempts []string
				var mu sync.Mutex
				winner, loser := &dnsTestStream{}, &dnsTestStream{}
				dialer := transport.FuncStreamDialer(func(ctx context.Context, address string) (transport.StreamConn, error) {
					mu.Lock()
					attempts = append(attempts, address)
					mu.Unlock()
					host, _, _ := net.SplitHostPort(address)
					if net.ParseIP(host).To4() == nil {
						switch tc.v6 {
						case "refused":
							return nil, errors.New("network unreachable")
						case "stalled":
							<-ctx.Done()
							// A concurrent success after the winner must be closed.
							return loser, nil
						}
					}
					return winner, nil
				})
				start := time.Now()
				conn, err := dialResolvedIPs(context.Background(), tc.network, tc.address, tc.ips, dialer)
				synctest.Wait()
				if err != nil || conn != winner || !slices.Equal(attempts, tc.want) || time.Since(start) != tc.delay {
					t.Fatalf("attempts=%v elapsed=%v conn=%v error=%v", attempts, time.Since(start), conn, err)
				}
				if winner.closed.Load() || (tc.v6 == "stalled" && !loser.closed.Load()) {
					t.Fatal("incorrect connection ownership")
				}
				conn.Close()
			})
		})
	}
}

func TestDNSHappyEyeballsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts, canceled atomic.Int32
		dialer := transport.FuncStreamDialer(func(ctx context.Context, _ string) (transport.StreamConn, error) {
			attempts.Add(1)
			<-ctx.Done()
			canceled.Add(1)
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		conn, err := dialResolvedIPs(ctx, "tcp", "example.invalid:443", []string{"192.0.2.1", "2001:db8::1"}, dialer)
		synctest.Wait()
		if conn != nil || !errors.Is(err, context.DeadlineExceeded) || attempts.Load() != 1 || canceled.Load() != 1 {
			t.Fatalf("conn=%v error=%v attempts=%d canceled=%d", conn, err, attempts.Load(), canceled.Load())
		}
	})
}

func TestDNSDialRejectsWrongFamilyAndNetwork(t *testing.T) {
	for _, network := range []string{"tcp4", "udp"} {
		dialer := transport.FuncStreamDialer(func(context.Context, string) (transport.StreamConn, error) {
			t.Error("invalid request caused a dial")
			return &dnsTestStream{}, nil
		})
		if conn, err := dialResolvedIPs(context.Background(), network, "[2001:db8::1]:443", []string{"2001:db8::1"}, dialer); conn != nil || err == nil {
			t.Fatal(conn, err)
		}
	}
}
