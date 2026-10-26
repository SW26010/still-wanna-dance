package console

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type ShutdownAction string

const (
	ShutdownExit          ShutdownAction = "exit"
	ShutdownRestart       ShutdownAction = "restart"
	lifecycleWriteTimeout                = time.Second
)

// A single immutable decision drives the owner, acknowledgements and observers.
// Closing the transport without a decision does not claim a planned exit.
type applicationLifecycle struct {
	once      sync.Once
	action    ShutdownAction // published by closing requested; never changed again
	requested chan struct{}
	mu        sync.Mutex
	closing   bool
	stop      chan struct{}
	drained   chan struct{}
	clients   map[*websocket.Conn]struct{}
}

func newApplicationLifecycle() *applicationLifecycle {
	return &applicationLifecycle{requested: make(chan struct{}), stop: make(chan struct{}), drained: make(chan struct{}), clients: make(map[*websocket.Conn]struct{})}
}

func (l *applicationLifecycle) snapshot() ShutdownAction {
	select {
	case <-l.requested:
		return l.action
	default:
		return "running"
	}
}

func (l *applicationLifecycle) request(action ShutdownAction, acknowledge func(ShutdownAction)) {
	acknowledged := false
	l.once.Do(func() {
		// Flush the winning HTTP response before waking the application owner.
		// No Console/producer lock is held during the bounded socket write.
		if acknowledge != nil {
			acknowledge(action)
			acknowledged = true
		}
		l.action = action
		slog.Info("application_shutdown_requested", "action", action)
		close(l.requested)
	})
	if acknowledge != nil && !acknowledged {
		acknowledge(l.snapshot())
	}
}

func (l *applicationLifecycle) attach(conn *websocket.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.clients[conn] = struct{}{}
	return true
}

func (l *applicationLifecycle) detach(conn *websocket.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, conn)
	if l.closing && len(l.clients) == 0 {
		close(l.drained)
	}
}

func (l *applicationLifecycle) beginShutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closing {
		l.closing = true
		close(l.stop)
		if len(l.clients) == 0 {
			close(l.drained)
		}
	}
}

// HTTP Shutdown does not own upgraded connections. Stop admission, let pending
// notifications finish, then force-close any peers that outlive the owner's grace.
func (l *applicationLifecycle) close(ctx context.Context) {
	l.beginShutdown()
	select {
	case <-l.drained:
		return
	case <-ctx.Done():
	}
	l.mu.Lock()
	clients := make([]*websocket.Conn, 0, len(l.clients))
	for conn := range l.clients {
		clients = append(clients, conn)
	}
	l.mu.Unlock()
	for _, conn := range clients {
		_ = conn.CloseNow()
	}
}

func (c *Console) ShutdownRequested() <-chan struct{} { return c.lifecycle.requested }
func (c *Console) ShutdownAction() ShutdownAction     { return c.lifecycle.snapshot() }
func (c *Console) RequestExit()                       { c.lifecycle.request(ShutdownExit, nil) }

func (c *Console) serveLifecycle(w http.ResponseWriter, r *http.Request) {
	// This read-only notification is also available on the consent page. Native
	// WebSocket supplies Origin, not the API token header; never put tokens in URLs.
	if r.Method != http.MethodGet || r.Header.Get("Origin") != "http://"+c.address {
		http.Error(w, "invalid lifecycle origin or method", http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		http.Error(w, "invalid fetch site", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if !c.lifecycle.attach(conn) {
		_ = conn.CloseNow()
		return
	}
	defer func() { _ = conn.CloseNow(); c.lifecycle.detach(conn) }()
	ctx, cancelRead := context.WithCancel(r.Context())
	defer cancelRead()
	go func() {
		// Reader handles control frames. Any data message ends this read-only
		// subscription. CloseRead in v1.8.12 calls Close from its own reader,
		// which waits for that same goroutine and defeats bounded cleanup.
		defer cancelRead()
		_, _, _ = conn.Reader(ctx)
	}()
	for {
		action := c.lifecycle.snapshot()
		select {
		case <-c.lifecycle.stop:
			// The decision can be published between the snapshot and stop.
			action = c.lifecycle.snapshot()
			if action == "running" {
				return
			}
		default:
		}
		writeCtx, cancel := context.WithTimeout(ctx, lifecycleWriteTimeout)
		err := wsjson.Write(writeCtx, conn, struct {
			Action ShutdownAction `json:"action"`
		}{action})
		cancel()
		if err != nil {
			return
		}
		if action != "running" {
			// The notification carries the decision. The deferred CloseNow must
			// not wait for a peer handshake: CloseNow cannot interrupt Close.
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-c.lifecycle.requested:
		case <-c.lifecycle.stop:
		}
	}
}
