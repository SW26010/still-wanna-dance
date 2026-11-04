package upstreamnet

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func probePeer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			serve(conn)
		}
	}()
	return listener.Addr().String()
}

func TestSOCKS5ServiceNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
		method, version, status  byte
		wantAuth, wantError      bool
	}{
		{name: "no authentication", method: 0},
		{name: "password", username: "测试用户", password: "private-secret", method: 2, version: 1, wantAuth: true},
		{name: "server chooses no authentication", username: "user", password: "private-secret", method: 0},
		{name: "wrong password", username: "user", password: "private-secret", method: 2, version: 1, status: 1, wantError: true},
		{name: "invalid auth version", username: "user", password: "private-secret", method: 2, version: 5, wantError: true},
		{name: "missing credentials", method: 2, wantError: true},
		{name: "methods rejected", method: 255, wantError: true},
		{name: "unsupported method", method: 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			address := probePeer(t, func(conn net.Conn) {
				defer close(done)
				greeting := make([]byte, 3)
				if tc.username != "" {
					greeting = make([]byte, 4)
				}
				if _, err := io.ReadFull(conn, greeting); err != nil {
					t.Error(err)
					return
				}
				want := string([]byte{5, 1, 0})
				if tc.username != "" {
					want = string([]byte{5, 2, 0, 2})
				}
				if string(greeting) != want {
					t.Errorf("wrong greeting: %v", greeting)
					return
				}
				// Fragment replies to exercise stream reads.
				conn.Write([]byte{5})
				conn.Write([]byte{tc.method})
				if tc.method == 2 && tc.username != "" {
					auth := make([]byte, 3+len(tc.username)+len(tc.password))
					if _, err := io.ReadFull(conn, auth); err != nil {
						t.Error(err)
						return
					}
					want := append([]byte{1, byte(len(tc.username))}, tc.username...)
					want = append(want, byte(len(tc.password)))
					want = append(want, tc.password...)
					if string(auth) != string(want) {
						t.Error("wrong auth request")
						return
					}
					conn.Write([]byte{tc.version, tc.status})
				}
				var extra [1]byte
				if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
					t.Errorf("probe must close without CONNECT: n=%d err=%v", n, err)
				}
			})
			auth, err := CheckSOCKS5Service(context.Background(), address, tc.username, tc.password)
			if auth != tc.wantAuth || (err != nil) != tc.wantError {
				t.Fatalf("authenticated=%v err=%v", auth, err)
			}
			if err != nil && (strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "测试用户")) {
				t.Fatal("credentials leaked")
			}
			<-done
		})
	}
}

func TestSOCKS5ServiceMalformedAndInterrupted(t *testing.T) {
	for _, reply := range [][]byte{{4, 0}, {5}} {
		address := probePeer(t, func(conn net.Conn) { greeting := make([]byte, 3); io.ReadFull(conn, greeting); conn.Write(reply) })
		if _, err := CheckSOCKS5Service(context.Background(), address, "", ""); err == nil {
			t.Fatal("accepted malformed reply")
		}
	}
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[timeout], func(t *testing.T) {
			ready, closed := make(chan struct{}), make(chan struct{})
			address := probePeer(t, func(conn net.Conn) {
				defer close(closed)
				greeting := make([]byte, 3)
				io.ReadFull(conn, greeting)
				close(ready)
				var b [1]byte
				conn.Read(b[:])
			})
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := CheckSOCKS5Service(ctx, address, "", ""); result <- err }()
			<-ready
			if !timeout {
				cancel()
			}
			want := context.Canceled
			if timeout {
				want = context.DeadlineExceeded
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("probe did not stop")
			}
			<-closed
		})
	}
}
