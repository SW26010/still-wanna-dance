package mihomo

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type blockedCloseConn struct {
	net.Conn
	entered, release chan struct{}
	once             sync.Once
}

func (c *blockedCloseConn) Close() error {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Close()
}

func waitSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not complete")
	}
}

func TestAdmissionLateBlockedCloseKeepsCapacity(t *testing.T) {
	for _, nativeError := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[nativeError], func(t *testing.T) {
			a := newAdmission(1, time.Second)
			left, right := net.Pipe()
			defer right.Close()
			conn := &blockedCloseConn{Conn: left, entered: make(chan struct{}), release: make(chan struct{})}
			defer close(conn.release)
			entered, release := make(chan struct{}), make(chan struct{})
			dial := a.wrap(func(context.Context, string, string) (net.Conn, error) {
				close(entered)
				<-release
				if nativeError {
					return conn, errors.New("private credential")
				}
				return conn, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := dial(ctx, "tcp", "target.invalid:80"); done <- err }()
			waitSignal(t, entered)
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			close(release)
			waitSignal(t, conn.entered)
			for range 100 {
				if _, err := dial(context.Background(), "tcp", "target.invalid:80"); !errors.Is(err, upstreamrequest.ErrCapacity) {
					t.Fatal("cleanup released slot early", err)
				}
			}
			pending := a.retire()
			select {
			case <-pending:
				t.Fatal("reported drained during Close")
			default:
			}
			if _, err := dial(context.Background(), "tcp", "target.invalid:80"); !errors.Is(err, upstreamrequest.ErrRetired) {
				t.Fatal(err)
			}
		})
	}
}

func TestAdmissionRetireOwnsDeliveredAndUncooperativeDial(t *testing.T) {
	a := newAdmission(2, 30*time.Millisecond)
	left, right := net.Pipe()
	defer right.Close()
	dial := a.wrap(func(context.Context, string, string) (net.Conn, error) { return left, nil })
	conn, err := dial(context.Background(), "tcp", "target.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	stuck := a.wrap(func(context.Context, string, string) (net.Conn, error) {
		close(entered)
		<-release
		return nil, errors.New("private")
	})
	done := make(chan error, 1)
	go func() { _, err := stuck(context.Background(), "tcp", "target.invalid:80"); done <- err }()
	waitSignal(t, entered)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("native timeout lost", err)
	}
	pending := a.retire()
	select {
	case <-pending:
		t.Fatal("uncooperative dial was forgotten")
	default:
	}
	close(release)
	waitSignal(t, pending)
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Fatal("retired delivered connection alive")
	}
}

func TestAdmissionIndependentExitsAndCancellationRace(t *testing.T) {
	a, b := newAdmission(1, time.Second), newAdmission(1, time.Second)
	left, right := net.Pipe()
	defer right.Close()
	dial := a.wrap(func(context.Context, string, string) (net.Conn, error) { return left, nil })
	c, err := dial(context.Background(), "tcp", "target.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dial(context.Background(), "tcp", "target.invalid:80"); !errors.Is(err, upstreamrequest.ErrCapacity) {
		t.Fatal(err)
	}
	other := b.wrap(func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("remote failure") })
	if _, err := other(context.Background(), "tcp", "target.invalid:80"); !errors.Is(err, ErrDial) {
		t.Fatal(err)
	}
	_ = c.Close()
	waitSignal(t, a.retire())
	waitSignal(t, b.retire())
	for range 200 {
		a := newAdmission(1, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		left, right := net.Pipe()
		dial := a.wrap(func(context.Context, string, string) (net.Conn, error) { cancel(); return left, nil })
		c, err := dial(ctx, "tcp", "target.invalid:80")
		if c != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("delivered after cancellation", err)
		}
		waitSignal(t, a.retire())
		right.Close()
	}
}
