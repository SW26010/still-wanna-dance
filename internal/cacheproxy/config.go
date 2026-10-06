package cacheproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"still-wanna-dance/internal/videometa"
)

// Config controls the local video service. Origins are dial addresses, not URLs;
// by default, valid API-returned hosts are connected through the selected dialer.
// Overrides retain the original HTTP Host and TLS identity; environment proxies
// are not used.
type Config struct {
	// BeginResourceLoad marks real upstream video work, including prefetch.
	// Release is called after the response closes; local hits never call it.
	BeginResourceLoad func() func()
	// BeginVideoRequest captures an observation window at HTTP arrival. The
	// returned callback runs only after validating a GET/HEAD video or playback
	// API request, before upstream resolution; it does not imply success.
	// Prefetch and HTTPS relay never call it.
	BeginVideoRequest func(time.Time) func()
	StorageDir        string
	Origins           map[string]string
	// IsResourceHost checks the current API-derived resource domain set.
	IsResourceHost func(string) bool
	// OriginScheme defaults to HTTPS. HTTP is restricted to loopback test origins.
	OriginScheme    string
	ResolvePlayback func(context.Context, string, string) (string, error)
	DownloadTimeout time.Duration
	MaxFileBytes    int64
	// MaxCacheBytes bounds retained videos in the canonical video store; zero is unlimited.
	MaxCacheBytes int64
	// RequestRetentionDays bounds request details; zero keeps them indefinitely.
	RequestRetentionDays int
	MaxDownloads         int
	Logger               *slog.Logger
	// DialContext supplies upstream connections (independent DNS or SOCKS5).
	DialContext func(context.Context, string, string) (net.Conn, error)
	// ResourceTransports supplies measured channels for this exact URL's host.
	// It never resolves a song or changes the resource address/content.
	ResourceTransports func(string) []http.RoundTripper
}

func DefaultConfig() Config {
	return Config{
		RequestRetentionDays: 30,
		OriginScheme:         "https",
		StorageDir:           "still-wanna-dance-data",
		Origins:              make(map[string]string),
		DownloadTimeout:      10 * time.Minute,
		MaxFileBytes:         2 << 30,
		MaxDownloads:         3,
		Logger:               slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
}

func (c Config) validate() error {
	if c.OriginScheme != "https" && c.OriginScheme != "http" {
		return errors.New("invalid origin scheme")
	}
	if c.RequestRetentionDays < 0 || c.RequestRetentionDays > 36500 {
		return errors.New("request retention must be between 0 and 36500 days")
	}
	if c.MaxCacheBytes < 0 {
		return errors.New("cache limit cannot be negative")
	}
	if c.StorageDir == "" || c.DownloadTimeout <= 0 || c.MaxFileBytes <= 0 || c.MaxFileBytes == int64(^uint64(0)>>1) || c.MaxDownloads < 1 {
		return errors.New("storage directory is required; timeout, file limit and download limit must be positive")
	}
	for host := range c.Origins {
		if !videometa.ValidHost(host) {
			return errors.New("invalid explicit origin domain")
		}
		addr, port, err := net.SplitHostPort(c.Origins[host])
		p, perr := strconv.Atoi(port)
		if err != nil || addr == "" || perr != nil || p < 1 || p > 65535 {
			return errors.New("each video host needs a valid upstream host:port")
		}
	}
	return nil
}

func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil || net.ParseIP(host) == nil {
				return nil, errors.New("upstream requires built-in DNS dialer")
			}
			return dialer.DialContext(ctx, network, address)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
	}
}
