package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"stepstash/internal/cacheproxy"
)

func main() {
	if err := run(); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	cfg := cacheproxy.DefaultConfig()
	flag.StringVar(&cfg.StatsPath, "stats-path", "", "SQLite usage statistics path (default: song library/.stepstash-usage.sqlite)")
	listen := flag.String("listen", "127.0.0.1:18080", "HTTP listen address (use 127.0.0.1:80 for game integration)")
	flag.StringVar(&cfg.CacheDir, "cache-dir", cfg.CacheDir, "owned cache directory; one process per directory")
	flag.StringVar(&cfg.SongsDir, "songs-dir", cfg.SongsDir, "song library root containing <id>/video.mp4; existing valid files are read directly")
	cf := flag.String("cf-origin", cfg.Origins["play.udon.dance"], "CF origin host:port, retaining Host play.udon.dance")
	nya := flag.String("hkg-origin", cfg.Origins["nya.xin.moe"], "HKG origin host:port, retaining Host nya.xin.moe")
	flag.DurationVar(&cfg.DownloadTimeout, "download-timeout", cfg.DownloadTimeout, "total per-file validation/download deadline")
	flag.Int64Var(&cfg.MaxFileBytes, "max-file-bytes", cfg.MaxFileBytes, "maximum accepted s parameter")
	flag.IntVar(&cfg.MaxDownloads, "max-downloads", cfg.MaxDownloads, "maximum concurrent distinct files")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	cfg.Origins["play.udon.dance"] = *cf
	cfg.Origins["nya.xin.moe"] = *nya
	service, err := cacheproxy.New(cfg)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: service, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	defer func() { resultErr = errors.Join(resultErr, closeVideoServer(server, service, 5*time.Second)) }()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	cfg.Logger.Info("listening", "address", listener.Addr().String(), "cache_dir", cfg.CacheDir)
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	return nil
}

// Close network connections before waiting for handlers and flushing statistics.
// The caller also uses this path when Serve exits unexpectedly.
func closeVideoServer(server *http.Server, service *cacheproxy.Server, grace time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		closeErr := server.Close()
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		err = errors.Join(err, closeErr)
	}
	return errors.Join(err, service.Close())
}
