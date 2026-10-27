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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/legal"
	"still-wanna-dance/internal/upstreamnet"
)

func main() {
	slog.SetDefault(applog.New(os.Stderr, applog.Info))
	if err := run(); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	logLevel := flag.String("log-level", "info", "application log level: off, error, warn, info, debug")
	showTerms := flag.Bool("show-terms", false, "print usage terms and exit")
	acceptTerms := flag.String("accept-terms", "", "explicitly accept the terms version shown by -show-terms")
	cfg := cacheproxy.DefaultConfig()
	resolver := upstreamnet.NewResolver(nil)
	defer resolver.Close()
	cfg.DialContext = resolver.DialContext
	socks5Address := flag.String("socks5-proxy", "", "SOCKS5 proxy host:port; delegates destination DNS to proxy; empty uses built-in DoH directly")
	socks5Username := flag.String("socks5-username", "", "SOCKS5 username; set password via SWD_SOCKS5_PASSWORD")
	listen := flag.String("listen", "127.0.0.1:18080", "HTTP listen address (use 127.0.0.1:80 for game integration)")
	database := flag.String("database", "", "existing SWD database file; opened without creating or migrating data")
	createDatabase := flag.Bool("create-database", false, "create the new file selected by -database and exit; never overwrite an existing file")
	flag.StringVar(&cfg.VideoDir, "video-dir", "", "existing complete-video directory, independent of the database; work files stay beside the executable")
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
	flag.IntVar(&cfg.MaxLocalValidations, "max-local-validations", cfg.MaxLocalValidations, "maximum concurrent local file validations (does not limit upstream downloads)")
	flag.Int64Var(&cfg.MaxCacheBytes, "max-cache-bytes", cfg.MaxCacheBytes, "local persistent video storage budget in bytes; 0 disables retention; excludes block cache, temporary files and database")
	flag.BoolVar(&cfg.CacheUnlimited, "cache-unlimited", cfg.CacheUnlimited, "retain local videos without a storage budget limit (mutually exclusive with -max-cache-bytes when true)")
	flag.BoolVar(&cfg.RetainFavorites, "retain-favorites", cfg.RetainFavorites, "allow favorite videos to exceed the storage budget (does not start downloads)")
	flag.StringVar(&cfg.AggressiveCacheReclamation, "aggressive-cache-reclamation", cfg.AggressiveCacheReclamation, "block cache reclamation: auto (on only with zero video budget), on, off")
	flag.IntVar(&cfg.RequestRetentionDays, "request-retention-days", cfg.RequestRetentionDays, "request detail retention in days; 0 disables retention (summaries are always retained)")
	flag.BoolVar(&cfg.RequestRetentionUnlimited, "request-retention-unlimited", cfg.RequestRetentionUnlimited, "retain request details indefinitely (mutually exclusive with -request-retention-days when true)")
	flag.Parse()
	level, err := applog.ParseLevel(*logLevel)
	if err != nil {
		return err
	}
	cfg.Logger = applog.New(os.Stderr, level)
	slog.SetDefault(cfg.Logger)
	limitSet, unlimitedSet := false, false
	requestDaysSet, requestUnlimitedSet := false, false
	flag.Visit(func(f *flag.Flag) {
		limitSet = limitSet || f.Name == "max-cache-bytes"
		unlimitedSet = unlimitedSet || f.Name == "cache-unlimited"
		requestDaysSet = requestDaysSet || f.Name == "request-retention-days"
		requestUnlimitedSet = requestUnlimitedSet || f.Name == "request-retention-unlimited"
	})
	if limitSet {
		if unlimitedSet && cfg.CacheUnlimited {
			return fmt.Errorf("-max-cache-bytes cannot be combined with -cache-unlimited=true")
		}
		cfg.CacheUnlimited = false
	}
	if requestDaysSet {
		if requestUnlimitedSet && cfg.RequestRetentionUnlimited {
			return fmt.Errorf("-request-retention-days cannot be combined with -request-retention-unlimited=true")
		}
		cfg.RequestRetentionUnlimited = false
	}
	if *showTerms {
		fmt.Print(legal.Text)
		return nil
	}
	if !*createDatabase {
		if err := legal.CheckAcceptance(*acceptTerms); err != nil {
			return err
		}
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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*database) == "" {
		return errors.New("-database is required; use -create-database only to create a new database")
	}
	*database, err = filepath.Abs(*database)
	if err != nil {
		return err
	}
	if err := apppaths.ValidateWorkSeparation(exe, *database); err != nil {
		return err
	}
	if *createDatabase {
		store, err := cacheproxy.CreateStore(*database)
		if err != nil {
			return err
		}
		if err := store.Close(); err != nil {
			return err
		}
		cfg.Logger.Info("database_created", "database", *database)
		return nil
	}
	if cfg.VideoDir == "" {
		return errors.New("-video-dir is required; select an existing complete-video directory")
	}
	cfg.VideoDir, err = filepath.Abs(cfg.VideoDir)
	if err != nil {
		return err
	}
	if err := apppaths.ValidateWorkSeparation(exe, cfg.VideoDir); err != nil {
		return err
	}
	work, err := cacheproxy.OpenWorkDirectory(apppaths.WorkForService(exe, *database, cfg.VideoDir), nil)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, work.Close()) }()
	cfg.Work = work
	if err := checkVideoPolicy(cfg.VideoDir, limitSet || unlimitedSet); err != nil {
		return err
	}
	store, err := cacheproxy.OpenStore(*database)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	cfg.Store = store
	service, err := cacheproxy.New(cfg)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: service, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelError)}
	defer func() { resultErr = errors.Join(resultErr, closeVideoServer(server, service, 5*time.Second)) }()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	cfg.Logger.Info("listening", "address", listener.Addr().String(), "database", *database, "video_dir", cfg.VideoDir)
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
