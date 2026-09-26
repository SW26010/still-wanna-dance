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
	"stepstash/internal/console"
)

func main() {
	if err := run(); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	cfg := cacheproxy.DefaultConfig()
	cfg.DialContext = console.NewUpstreamDialer()
	listen := flag.String("listen", "127.0.0.1:18080", "HTTP listen address (use 127.0.0.1:80 for game integration)")
	flag.StringVar(&cfg.StorageDir, "storage-dir", cfg.StorageDir, "storage root (videos, tmp and stepstash.sqlite); one process per root")
	cf := flag.String("cf-origin", cfg.Origins["play.udon.dance"], "CF HTTPS origin host:port; retains Host and TLS SNI play.udon.dance")
	nya := flag.String("hkg-origin", cfg.Origins["nya.xin.moe"], "HKG HTTPS origin host:port; retains Host and TLS SNI nya.xin.moe")
	flag.DurationVar(&cfg.DownloadTimeout, "download-timeout", cfg.DownloadTimeout, "shared cache task timeout (validation, download and publication); excludes admission wait and response transfer")
	flag.Int64Var(&cfg.MaxFileBytes, "max-file-bytes", cfg.MaxFileBytes, "maximum video size in bytes (s URL parameter)")
	flag.IntVar(&cfg.MaxDownloads, "max-downloads", cfg.MaxDownloads, "maximum concurrent shared cache tasks; also limits local file validation concurrency")
	flag.Int64Var(&cfg.MaxCacheBytes, "max-cache-bytes", cfg.MaxCacheBytes, "retained video limit in bytes; 0 is unlimited; excludes temporary files and database")
	flag.IntVar(&cfg.RequestRetentionDays, "request-retention-days", cfg.RequestRetentionDays, "request detail retention in days; 0 is unlimited (summaries are always retained)")
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
	cfg.Logger.Info("listening", "address", listener.Addr().String(), "storage_dir", cfg.StorageDir)
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
