package console

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"still-wanna-dance/internal/upstreamrequest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real SOCKS5 wire peer: asserts that destinations arrive as domain names,
// without consulting any resolver or contacting public upstreams.
func socksPeer(t *testing.T, handle func(net.Conn, string)) string {
	return socksPeerWithAuth(t, "", "", handle)
}

func socksPeerWithAuth(t *testing.T, username, password string, handle func(net.Conn, string)) string {
	t.Helper()
	l := relayListener(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	connections := make(map[net.Conn]bool)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				defer func() { mu.Lock(); delete(connections, conn); mu.Unlock() }()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var greeting [2]byte
				if _, err := io.ReadFull(conn, greeting[:]); err != nil {
					return
				}
				methods := make([]byte, int(greeting[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				if greeting[0] != 5 {
					t.Error("not SOCKS5")
					return
				}
				method := byte(0)
				if username != "" {
					method = 2
				}
				if _, err := conn.Write([]byte{5, method}); err != nil {
					return
				}
				if method == 2 {
					var header [2]byte
					if _, err := io.ReadFull(conn, header[:]); err != nil {
						return
					}
					user := make([]byte, int(header[1]))
					if _, err := io.ReadFull(conn, user); err != nil {
						return
					}
					var length [1]byte
					if _, err := io.ReadFull(conn, length[:]); err != nil {
						return
					}
					secret := make([]byte, int(length[0]))
					if _, err := io.ReadFull(conn, secret); err != nil {
						return
					}
					if header[0] != 1 || string(user) != username || string(secret) != password {
						conn.Write([]byte{1, 1})
						return
					}
					if _, err := conn.Write([]byte{1, 0}); err != nil {
						return
					}
				}
				var header [5]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil {
					return
				}
				if header[0] != 5 || header[1] != 1 || header[3] != 3 {
					t.Errorf("expected SOCKS5 CONNECT with domain: %v", header)
					return
				}
				destination := make([]byte, int(header[4])+2)
				if _, err := io.ReadFull(conn, destination); err != nil {
					return
				}
				n := len(destination) - 2
				handle(conn, net.JoinHostPort(string(destination[:n]), strconv.Itoa(int(binary.BigEndian.Uint16(destination[n:])))))
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-done
		mu.Lock()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return l.Addr().String()
}

func socksReply(conn net.Conn, status byte) {
	conn.Write([]byte{5, status, 0, 1, 127, 0, 0, 1, 0, 0})
}

func TestSOCKS5RelayDrainsResponseAfterClientHalfClose(t *testing.T) {
	const request = "request before FIN"
	response := strings.Repeat("response after FIN\n", 16384)
	address := socksPeer(t, func(conn net.Conn, _ string) {
		socksReply(conn, 0)
		body, err := io.ReadAll(conn)
		if err != nil || string(body) != request {
			t.Errorf("request before half-close: %q, %v", body, err)
			return
		}
		// Reply only after observing FIN, so premature full-close cannot pass.
		io.WriteString(conn, response)
	})
	dial, err := NewSOCKS5Dialer(address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	upstream, err := dial(ctx, "tcp", "unresolvable.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	l := relayListener(t)
	defer l.Close()
	client, err := net.DialTCP("tcp4", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	local, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	done := make(chan struct{}, 2)
	go func() { relayCopy(upstream, local); done <- struct{}{} }()
	go func() { relayCopy(local, upstream); done <- struct{}{} }()
	defer func() { local.Close(); upstream.Close(); <-done; <-done }()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(client)
	if err != nil || string(body) != response {
		t.Fatalf("response truncated after client FIN: received %d/%d bytes, error %v", len(body), len(response), err)
	}
}

func TestSOCKS5DelegatesDNSAndHonorsCancellation(t *testing.T) {
	for _, mode := range []string{"success", "rejected", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			requested := make(chan string, 1)
			address := socksPeer(t, func(conn net.Conn, target string) {
				requested <- target
				if mode == "canceled" {
					io.Copy(io.Discard, conn)
					return
				}
				if mode == "rejected" {
					socksReply(conn, 5)
					return
				}
				socksReply(conn, 0)
				io.WriteString(conn, "proxied")
			})
			dial, err := NewSOCKS5Dialer(address)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if mode == "canceled" {
				go func() {
					select {
					case <-requested:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			conn, err := dial(ctx, "tcp", "unresolvable.invalid:443")
			if mode == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
				return
			}
			select {
			case got := <-requested:
				if got != "unresolvable.invalid:443" {
					t.Fatal(got)
				}
			case <-ctx.Done():
				t.Fatal("proxy did not receive destination", err)
			}
			if mode == "rejected" {
				if err == nil {
					conn.Close()
					t.Fatal("proxy rejection ignored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			body, err := io.ReadAll(conn)
			if err != nil || string(body) != "proxied" {
				t.Fatalf("%q: %v", body, err)
			}
		})
	}
}

func TestSOCKS5SettingsValidation(t *testing.T) {
	for _, address := range []string{"", "localhost", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "socks5://127.0.0.1:1080", "user@localhost:1080", "bad host:1080"} {
		if _, err := absoluteSettings(Settings{StorageDir: t.TempDir(), UpstreamMode: "socks5", SOCKS5Address: address}); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:1080", "localhost:1080", "[::1]:1080"} {
		if _, err := NewSOCKS5Dialer(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	if _, err := absoluteSettings(Settings{StorageDir: t.TempDir(), UpstreamMode: "unknown"}); err == nil {
		t.Fatal("accepted unknown mode")
	}
}

func TestSOCKS5FailureDoesNotContactReachableDestination(t *testing.T) {
	var directRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directRequests.Add(1) }))
	defer origin.Close()
	_, port, _ := net.SplitHostPort(origin.Listener.Addr().String())
	target := "localhost:" + port
	for _, unavailable := range []bool{false, true} {
		address := socksPeer(t, func(conn net.Conn, got string) {
			if got != target {
				t.Errorf("destination: %s", got)
			}
			socksReply(conn, 5)
		})
		if unavailable {
			l := relayListener(t)
			address = l.Addr().String()
			l.Close()
		}
		dial, err := NewSOCKS5Dialer(address)
		if err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{DialContext: dial}
		client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + target)
		transport.CloseIdleConnections()
		if err == nil {
			resp.Body.Close()
			t.Fatal("proxy failure fell back to direct")
		}
	}
	if directRequests.Load() != 0 {
		t.Fatal("destination contacted directly")
	}
}

func TestConsoleNetworkSwitchPreservesInflightRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	address := socksPeer(t, func(conn net.Conn, _ string) {
		socksReply(conn, 0)
		close(started)
		<-release
		io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nold")
	})
	defer unblock()
	c := testConsole(t)
	s := c.settings
	s.UpstreamMode, s.SOCKS5Address = "socks5", address
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := c.upstreamClient().Get("http://unresolvable.invalid/coverage")
		if err == nil {
			defer resp.Body.Close()
			var body []byte
			body, err = io.ReadAll(resp.Body)
			if err == nil && string(body) != "old" {
				err = fmt.Errorf("body: %q", body)
			}
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	s.UpstreamMode = "direct"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConsoleSOCKS5APIAndHTTPSRelay(t *testing.T) {
	cert, roots := relayCertificate(t)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.ServerName != r.Host {
			t.Errorf("Host/SNI mismatch: %s / %s", r.Host, r.TLS.ServerName)
		}
		io.WriteString(w, "through proxy")
	}))
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	backend.StartTLS()
	defer backend.Close()
	var calls atomic.Int32
	address := socksPeer(t, func(conn net.Conn, target string) {
		if target != "api.udon.dance:443" && target != "nya.xin.moe:443" {
			t.Errorf("unexpected target: %s", target)
			return
		}
		calls.Add(1)
		upstream, err := net.DialTimeout("tcp", backend.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.Close()
		socksReply(conn, 0)
		done := make(chan struct{})
		go func() { io.Copy(upstream, conn); upstream.Close(); close(done) }()
		io.Copy(conn, upstream)
		conn.Close()
		<-done
	})
	c := testConsole(t)
	s := c.settings
	s.UpstreamMode, s.SOCKS5Address = "socks5", address
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	c.client.Transport.(*upstreamrequest.Transport).RoundTripper.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	check := func(client *http.Client, target string) {
		t.Helper()
		resp, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "through proxy" {
			t.Fatalf("body %q: %v", body, err)
		}
	}
	check(c.upstreamClient(), "https://api.udon.dance/test")
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, c.https.listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	check(&http.Client{Transport: transport, Timeout: 3 * time.Second}, "https://nya.xin.moe/test")
	if calls.Load() != 2 {
		t.Fatalf("got %d proxy connections", calls.Load())
	}
}

func TestConsoleSOCKS5CacheAndSettingsSwitch(t *testing.T) {
	requested := make(chan string, 16)
	address := socksPeer(t, func(conn net.Conn, target string) { requested <- target; socksReply(conn, 5) })
	c := testConsole(t)
	if c.settings.UpstreamMode != "direct" {
		t.Fatal("old config must default to direct")
	}
	originalClient := c.client
	s := c.settings
	s.UpstreamMode, s.SOCKS5Address = "socks5", address
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.UpstreamMode != "socks5" || loaded.settings.SOCKS5Address != address {
		t.Fatal("proxy settings not persisted")
	}
	if c.client == originalClient {
		t.Fatal("client was not replaced")
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.save(s); err == nil {
		t.Fatal("allowed switch while running")
	}
	target := fmt.Sprintf("http://play.udon.dance/files/1/2-video.mp4?e=%x&s=4", md5.Sum([]byte("test")))
	w := httptest.NewRecorder()
	c.service.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
	if w.Code < 400 {
		t.Fatalf("proxy rejection did not fail download: %d", w.Code)
	}
	select {
	case got := <-requested:
		if got != "play.udon.dance:443" {
			t.Fatal(got)
		}
	default:
		t.Fatal("cache engine bypassed SOCKS5")
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	proxyClient := c.client
	s.UpstreamMode = "direct"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	if c.service != nil || c.client == proxyClient {
		t.Fatal("network switch retained engine or client")
	}
	// Direct mode works again for a loopback destination without using the proxy.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer origin.Close()
	resp, err := c.upstreamClient().Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "direct" {
		t.Fatal(string(body))
	}
}
