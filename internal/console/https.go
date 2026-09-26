package console

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// Only the hosts managed by StepStash are forwarded. Keep the original SNI
// and encrypted bytes intact: certificates, HTTP and video remain end-to-end.
// The selected upstream dialer handles DNS and connections.
func httpsOrigin(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	switch name {
	case "api.udon.dance", "nya.xin.moe", "play.udon.dance":
		return net.JoinHostPort(name, "443")
	default:
		return ""
	}
}

type httpsRelay struct {
	listener net.Listener
	dial     func(context.Context, string, string) (net.Conn, error)
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	clients  map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func newHTTPSRelay(l net.Listener, dial func(context.Context, string, string) (net.Conn, error)) *httpsRelay {
	ctx, cancel := context.WithCancel(context.Background())
	return &httpsRelay{listener: l, dial: dial, ctx: ctx, cancel: cancel, clients: make(map[net.Conn]struct{})}
}

func (p *httpsRelay) serve() error {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return err
		}
		p.mu.Lock()
		if p.closed || len(p.clients) >= 128 {
			p.mu.Unlock()
			client.Close()
			continue
		}
		p.clients[client] = struct{}{}
		p.wg.Add(1)
		p.mu.Unlock()
		go func() {
			defer p.wg.Done()
			defer func() {
				client.Close()
				p.mu.Lock()
				delete(p.clients, client)
				p.mu.Unlock()
			}()
			if err := p.forward(client); err != nil && p.ctx.Err() == nil {
				slog.Debug("https_forward_failed", "error", err)
			}
		}()
	}
}

func (p *httpsRelay) close() {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	p.listener.Close()
	for c := range p.clients {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// Use Go's ClientHello parser, including fragmented TLS records. Stop before
// negotiation and suppress its alert: this is inspection, not TLS termination.
// Bound both memory and time for incomplete or malformed local connections.
type helloCapture struct {
	net.Conn
	reader io.Reader
	bytes  bytes.Buffer
}

func (c *helloCapture) Read(b []byte) (int, error) {
	n, err := c.reader.Read(b)
	c.bytes.Write(b[:n])
	return n, err
}

func (c *helloCapture) Write(b []byte) (int, error) { return len(b), nil }

func readClientHello(conn net.Conn) (string, []byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return "", nil, err
	}
	capture := &helloCapture{Conn: conn, reader: io.LimitReader(conn, 64<<10)}
	var name string
	parsed := errors.New("ClientHello captured")
	probe := tls.Server(capture, &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		name = hello.ServerName
		return nil, parsed
	}})
	if err := probe.Handshake(); !errors.Is(err, parsed) {
		return "", nil, err
	}
	return name, capture.bytes.Bytes(), nil
}

func (p *httpsRelay) forward(client net.Conn) error {
	name, hello, err := readClientHello(client)
	if err != nil {
		return err
	}
	origin := httpsOrigin(name)
	if origin == "" {
		return errors.New("HTTPS SNI is not a managed video domain")
	}
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	upstream, err := p.dial(ctx, "tcp4", origin)
	cancel()
	if err != nil {
		return err
	}
	defer upstream.Close()
	stop := context.AfterFunc(p.ctx, func() { upstream.Close() })
	defer stop()
	if err = upstream.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if _, err = io.Copy(upstream, bytes.NewReader(hello)); err != nil {
		return err
	}
	client.SetDeadline(time.Now().Add(2 * time.Minute))
	upstream.SetDeadline(time.Now().Add(2 * time.Minute))
	slog.Debug("https_forward_connected", "server_name", name, "origin", origin)
	// A moving idle deadline permits long video streams without retaining idle
	// tunnels indefinitely. Preserve TCP half-close so a final response drains.
	done := make(chan struct{})
	go func() {
		defer close(done)
		relayCopy(upstream, client)
	}()
	relayCopy(client, upstream)
	client.Close()
	upstream.Close()
	<-done
	return nil
}

type idleConn struct {
	net.Conn
	peer net.Conn
}

func (c idleConn) touch() {
	deadline := time.Now().Add(2 * time.Minute)
	c.SetDeadline(deadline)
	c.peer.SetDeadline(deadline)
}

func (c idleConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c idleConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func relayCopy(dst, src net.Conn) {
	_, err := io.Copy(idleConn{dst, src}, idleConn{src, dst})
	if writer, ok := dst.(interface{ CloseWrite() error }); ok && err == nil {
		if writer.CloseWrite() == nil {
			return
		}
	}
	dst.Close()
}
