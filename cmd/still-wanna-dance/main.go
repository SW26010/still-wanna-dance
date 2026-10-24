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
	"strings"
	"syscall"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/legal"
	"still-wanna-dance/internal/upstreamnet"
)

func main() {
	if err := run(); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	showTerms := flag.Bool("show-terms", false, "print usage terms and exit")
	acceptTerms := flag.String("accept-terms", "", "explicitly accept the terms version shown by -show-terms")
	cfg := cacheproxy.DefaultConfig()
	resolver := upstreamnet.NewResolver(nil)
	defer resolver.Close()
	cfg.DialContext = resolver.DialContext
	socks5Address := flag.String("socks5-proxy", "", "SOCKS5 proxy host:port; delegates destination DNS to proxy; empty uses built-in DoH directly")
	socks5Username := flag.String("socks5-username", "", "SOCKS5 username; set password via SWD_SOCKS5_PASSWORD")
	listen := flag.String("listen", "127.0.0.1:18080", "HTTP listen address (use 127.0.0.1:80 for game integration)")
	flag.StringVar(&cfg.StorageDir, "storage-dir", cfg.StorageDir, "storage root (videos, tmp and storage.sqlite); one process per root")
	flag.Func("origin", "explicit DOMAIN=HOST:PORT dial override (repeatable); retains DOMAIN as HTTP Host and TLS SNI", func(value string) error {
		host, address, ok := strings.Cut(value, "=")
		if !ok || host == "" || address == "" {
			return errors.New("origin must be DOMAIN=HOST:PORT")
		}
		cfg.Origins[host] = address
		return nil
	})
	flag.DurationVar(&cfg.DownloadTimeout, "download-timeout", cfg.DownloadTimeout, "timeout per upstream attempt (headers and body) or local validation/publication phase; excludes retries, scheduling waits and client response transfer")
	flag.Int64Var(&cfg.MaxFileBytes, "max-file-bytes", cfg.MaxFileBytes, "maximum video size in bytes (s URL parameter)")
	flag.IntVar(&cfg.MaxLocalValidations, "max-downloads", cfg.MaxLocalValidations, "maximum concurrent local file validations (does not limit upstream downloads)")
	flag.Int64Var(&cfg.MaxCacheBytes, "max-cache-bytes", cfg.MaxCacheBytes, "local persistent video storage budget in bytes; 0 disables retention; excludes block cache, temporary files and database")
	flag.BoolVar(&cfg.CacheUnlimited, "cache-unlimited", cfg.CacheUnlimited, "retain local videos without a storage budget limit (mutually exclusive with -max-cache-bytes when true)")
	flag.BoolVar(&cfg.RetainFavorites, "retain-favorites", cfg.RetainFavorites, "allow favorite videos to exceed the storage budget (does not start downloads)")
	flag.StringVar(&cfg.AggressiveCacheReclamation, "aggressive-cache-reclamation", cfg.AggressiveCacheReclamation, "block cache reclamation: auto (on only with zero video budget), on, off")
	flag.IntVar(&cfg.RequestRetentionDays, "request-retention-days", cfg.RequestRetentionDays, "request detail retention in days; 0 is unlimited (summaries are always retained)")
	flag.Parse()
	limitSet, unlimitedSet := false, false
	flag.Visit(func(f *flag.Flag) {
		limitSet = limitSet || f.Name == "max-cache-bytes"
		unlimitedSet = unlimitedSet || f.Name == "cache-unlimited"
	})
	if limitSet {
		if unlimitedSet && cfg.CacheUnlimited {
			return fmt.Errorf("-max-cache-bytes cannot be combined with -cache-unlimited=true")
		}
		cfg.CacheUnlimited = false
	}
	if *showTerms {
		fmt.Print(legal.Text)
		return nil
	}
	if err := legal.CheckAcceptance(*acceptTerms); err != nil {
		return err
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *socks5Username != "" && *socks5Address == "" {
		return fmt.Errorf("-socks5-username requires -socks5-proxy")
	}
	if *socks5Address != "" {
		var err error
		cfg.DialContext, err = upstreamnet.NewAuthenticatedSOCKS5Dialer(*socks5Address, *socks5Username, socks5Password())
		if err != nil {
			return err
		}
	}
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

func socks5Password() string {
	return os.Getenv("SWD_SOCKS5_PASSWORD")
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
