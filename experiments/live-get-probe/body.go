package main

// The body probe is a separate lab server. It never invokes the cache engine.
import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/upstreamnet"
)

type bodyMedia struct {
	Song string `json:"song"`
	File string `json:"file"`
	MD5  string `json:"md5"`
}

type bodyGate struct {
	mu                         sync.Mutex
	limit, reserved, delivered int64
}

func (g *bodyGate) take(ctx context.Context, want int) (int, error) {
	for {
		g.mu.Lock()
		n := int64(want)
		if g.limit >= 0 && n > g.limit-g.reserved {
			n = g.limit - g.reserved
		}
		if n > 0 {
			g.reserved += n
			g.mu.Unlock()
			return int(n), nil
		}
		g.mu.Unlock()
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func (g *bodyGate) finish(reserved, written int) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserved -= int64(reserved - written)
	g.delivered += int64(written)
	return g.delivered
}

type bodyWriter struct {
	http.ResponseWriter
	ctx           context.Context
	gate          *bodyGate
	log           *slog.Logger
	id            uint64
	status        int
	offset, bytes int64
}

func (w *bodyWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status == http.StatusPartialContent {
		fmt.Sscanf(w.Header().Get("Content-Range"), "bytes %d-", &w.offset)
	}
	w.log.Info("body_headers", "request_id", w.id, "status", status, "content_range", w.Header().Get("Content-Range"), "content_length", w.Header().Get("Content-Length"))
	w.ResponseWriter.WriteHeader(status)
}

func (w *bodyWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	// WriteHeader alone can stay buffered. Body quota must not delay headers,
	// including a new GET after another reader consumed the shared allowance.
	if w.bytes == 0 && w.gate != nil && (w.status == http.StatusOK || w.status == http.StatusPartialContent) {
		if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
			return 0, err
		}
	}
	total := 0
	for len(p) > 0 {
		n := min(len(p), quantum)
		var err error
		if w.gate != nil && (w.status == 200 || w.status == 206) {
			n, err = w.gate.take(w.ctx, n)
			if err != nil {
				return total, err
			}
		}
		written, writeErr := w.ResponseWriter.Write(p[:n])
		var gatedTotal int64
		if w.gate != nil && (w.status == 200 || w.status == 206) {
			gatedTotal = w.gate.finish(n, written)
		}
		flushErr := http.NewResponseController(w.ResponseWriter).Flush()
		w.log.Info("body_write", "request_id", w.id, "offset", w.offset, "bytes", written, "request_bytes", w.bytes+int64(written), "gated_total_bytes", gatedTotal, "write_ok", writeErr == nil, "flush_ok", flushErr == nil)
		w.offset += int64(written)
		w.bytes += int64(written)
		total += written
		if writeErr != nil {
			return total, writeErr
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		if flushErr != nil {
			return total, flushErr
		}
		p = p[n:]
	}
	return total, nil
}

func bodyHandler(media map[string]bodyMedia, gate *bodyGate, log *slog.Logger) http.Handler {
	var seq atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Api/Songs/play" || (r.Method != "GET" && r.Method != "HEAD") {
			http.NotFound(w, r)
			return
		}
		m, ok := media[r.URL.Query().Get("id")]
		if !ok {
			http.Error(w, "song is not in the isolated lab manifest", 404)
			return
		}
		id := seq.Add(1)
		player := strings.HasPrefix(r.UserAgent(), "NSPlayer/")
		log.Info("body_request", "request_id", id, "song", m.Song, "method", r.Method, "range", r.Header.Get("Range"), "player", player, "user_agent", r.UserAgent())
		// Match the product's single-range scope, never emit multipart framing as video bytes.
		if strings.Contains(r.Header.Get("Range"), ",") {
			http.Error(w, "multiple ranges unsupported", 416)
			return
		}
		f, err := os.Open(m.File)
		if err != nil {
			http.Error(w, "local media unavailable", 500)
			return
		}
		defer f.Close()
		bw := &bodyWriter{ResponseWriter: w, ctx: r.Context(), log: log, id: id}
		if player && r.Method == "GET" {
			bw.gate = gate
		}
		bw.Header().Set("Content-Type", "video/mp4")
		bw.Header().Set("ETag", `"`+m.MD5+`"`)
		bw.Header().Set("Cache-Control", "no-store")
		// Stable modification time preserves standard Range and conditional semantics.
		http.ServeContent(bw, r, "video.mp4", time.Unix(1, 0), f)
		log.Info("body_finished", "request_id", id, "bytes", bw.bytes, "canceled", r.Context().Err() != nil)
	})
}

func runBodyProbe(parent context.Context, address, httpsAddress, manifestPath, controlPath, stopFile string, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var entries []bodyMedia
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &entries); err != nil {
		return err
	}
	media := map[string]bodyMedia{}
	for _, m := range entries {
		if m.Song == "" || media[m.Song].Song != "" {
			return errors.New("missing or duplicate song")
		}
		f, err := os.Open(m.File)
		if err != nil {
			return err
		}
		h := md5.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != m.MD5 {
			return errors.New("local media MD5 mismatch")
		}
		media[m.Song] = m
		log.Info("body_media_verified", "song", m.Song, "md5", m.MD5, "bytes", n)
	}
	if len(media) == 0 {
		return errors.New("empty media manifest")
	}
	gate := &bodyGate{limit: -1}
	update := func() error {
		if controlPath == "" {
			return nil
		}
		b, err := os.ReadFile(controlPath)
		if err != nil {
			return err
		}
		var control struct {
			Limit *int64 `json:"limitBytes"`
		}
		if err = json.Unmarshal(b, &control); err != nil {
			return err
		}
		if control.Limit == nil || *control.Limit < -1 {
			return errors.New("invalid limitBytes")
		}
		gate.mu.Lock()
		defer gate.mu.Unlock()
		if gate.limit != *control.Limit {
			log.Info("body_limit", "limit_bytes", *control.Limit, "delivered_bytes", gate.delivered)
		}
		gate.limit = *control.Limit
		return nil
	}
	if err = update(); err != nil {
		return err
	}
	l, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	defer l.Close()
	server := &http.Server{Handler: bodyHandler(media, gate, log), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	if httpsAddress != "" {
		host, _, err := net.SplitHostPort(httpsAddress)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("HTTPS relay must be loopback")
		}
		tlsListener, err := net.Listen("tcp4", httpsAddress)
		if err != nil {
			return err
		}
		defer tlsListener.Close()
		resolver := upstreamnet.NewResolver(nil)
		defer resolver.Close()
		go func() {
			for {
				c, err := tlsListener.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					stop := context.AfterFunc(ctx, func() { c.Close() })
					defer stop()
					u, err := resolver.DialContext(ctx, "tcp", "api.udon.dance:443")
					if err != nil {
						return
					}
					defer u.Close()
					go func() { io.Copy(u, c); u.Close() }()
					io.Copy(c, u)
				}()
			}
		}()
	}
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				server.Close()
				return
			case <-tick.C:
				if stopFile != "" {
					if _, err := os.Stat(stopFile); err == nil {
						cancel()
						continue
					}
				}
				if err := update(); err != nil {
					log.Error("body_control_failed")
					cancel()
				}
			}
		}
	}()
	log.Info("body_probe_ready", "listen", address, "gate_scope", "NSPlayer GET response bodies", "quantum", quantum)
	err = server.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
