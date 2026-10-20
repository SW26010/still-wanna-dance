package console

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	winio "github.com/tailscale/go-winio"
)

func TestHostsSessionChildProcess(t *testing.T) {
	pipe := os.Getenv("SWD_TEST_HOSTS_PIPE")
	if pipe == "" {
		return
	}
	parent, _ := strconv.ParseUint(os.Getenv("SWD_TEST_HOSTS_PARENT"), 10, 32)
	conn, err := dialHostsParent(pipe, uint32(parent))
	if err == nil {
		err = serveHostsSession(conn, os.Getenv("SWD_TEST_HOSTS_FILE"), func() {})
	}
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestWindowsHostsSessionRealProcessAndPipe(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, disconnect := range []bool{false, true} {
			path, launches := hostsFixture(t, hostsBaseline), 0
			var child *exec.Cmd
			done := make(chan error, 1)
			h := newHostsController(path, func(ctx context.Context) (hostsConnection, error) {
				return connectHostsSession(ctx, func(ctx context.Context, pipe string) (uint32, error) {
					launches++
					child = exec.Command(os.Args[0], "-test.run=^TestHostsSessionChildProcess$")
					child.Env = append(os.Environ(), "SWD_TEST_HOSTS_PIPE="+pipe, "SWD_TEST_HOSTS_FILE="+path, "SWD_TEST_HOSTS_PARENT="+strconv.Itoa(os.Getpid()))
					if err := child.Start(); err != nil {
						return 0, err
					}
					go func() { done <- child.Wait() }()
					return uint32(child.Process.Pid), nil
				})
			})
			if err := h.change("enable", restore); err != nil {
				t.Fatal(err)
			}
			if err := h.change("enable", restore); err != nil {
				t.Fatal(err)
			}
			if disconnect {
				if err := h.session.close(); err != nil {
					t.Fatal(err)
				}
			} else if err := h.close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				child.Process.Kill()
				t.Fatal("helper did not exit")
			}
			if (readHostsFixture(t, path) == hostsBaseline) != restore || launches != 1 {
				t.Fatalf("restore=%v disconnect=%v launches=%d", restore, disconnect, launches)
			}
			h.cancel()
		}
	}
}

func TestWindowsHostsPipeRejectsWrongParentAndInvalidNames(t *testing.T) {
	for _, pipe := range []string{`\\remote\pipe\SWD-hosts-01234567890123456789012345678901`, hostsPipePrefix + "bad"} {
		if conn, err := dialHostsParent(pipe, uint32(os.Getpid())); err == nil {
			conn.Close()
			t.Fatal("invalid pipe accepted")
		}
	}
	pipe := hostsPipePrefix + "01234567890123456789012345678901"
	l, err := winio.ListenPipe(pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		conn, err := l.Accept()
		if err == nil {
			defer conn.Close()
			<-done
		}
	}()
	defer close(done)
	if conn, err := dialHostsParent(pipe, uint32(os.Getpid()+1)); err == nil {
		conn.Close()
		t.Fatal("wrong server process accepted")
	}
}
