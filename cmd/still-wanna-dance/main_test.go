package main

import (
	"crypto/md5"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// net.Pipe has no socket buffering: a peer that does not read reliably blocks
// the real HTTP server's response writes without relying on OS buffer sizes.
type pipeListener struct {
	conn   chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conn:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

type observedWriter struct {
	http.ResponseWriter
	started chan struct{}
	once    sync.Once
}

func (w *observedWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.ResponseWriter.Write(b)
}

func TestShutdownClosesPausedReaderAndFlushesUsage(t *testing.T) {
	cfg := cacheproxy.DefaultConfig()
	cfg.OriginScheme = "http"
	cfg.StorageDir = t.TempDir()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	body := strings.Repeat("x", 128*1024)
	if err := os.MkdirAll(filepath.Join(cfg.StorageDir, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StorageDir, "videos", fmt.Sprintf("%x.mp4", md5.Sum([]byte(body)))), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.ServeHTTP(&observedWriter{ResponseWriter: w, started: started}, r)
	})}
	serverConn, client := net.Pipe()
	l := &pipeListener{conn: make(chan net.Conn, 1), closed: make(chan struct{})}
	l.conn <- serverConn
	t.Cleanup(func() { client.Close(); server.Close(); service.Close() })
	go server.Serve(l)
	client.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(client, "GET /files/1/1-v.mp4?e=%x&s=%d HTTP/1.1\r\nHost: play.udon.dance\r\n\r\n", md5.Sum([]byte(body)), len(body)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("response did not start")
	}
	done := make(chan error, 1)
	go func() { done <- closeVideoServer(server, service, 50*time.Millisecond) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stuck on paused reader")
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.StorageDir, "stepstash.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var events, gets int
	if err := db.QueryRow("SELECT count(*) FROM request_events WHERE source='http' AND outcome='canceled'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM song_usage").Scan(&gets); err != nil {
		t.Fatal(err)
	}
	if events != 1 || gets != 0 {
		t.Fatalf("statistics not flushed: events=%d gets=%d", events, gets)
	}
}

func TestSOCKS5PasswordAlias(t *testing.T) {
	t.Setenv("STEPSTASH_SOCKS5_PASSWORD", "legacy")
	t.Setenv("STILL_WANNA_DANCE_SOCKS5_PASSWORD", "current")
	if got := socks5Password(); got != "current" {
		t.Fatal("new variable must win")
	}
	t.Setenv("STILL_WANNA_DANCE_SOCKS5_PASSWORD", "")
	if got := socks5Password(); got != "" {
		t.Fatal("explicit empty must not fall back")
	}
	if err := os.Unsetenv("STILL_WANNA_DANCE_SOCKS5_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	if got := socks5Password(); got != "legacy" {
		t.Fatal("legacy fallback missing")
	}
}
