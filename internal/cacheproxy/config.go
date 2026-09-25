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
)

// Config controls the local video service. Origins are dial addresses, not URLs;
// the original HTTP Host is retained and environment proxies are not used.
type Config struct {
	CacheDir string
	SongsDir string
	// StatsPath defaults to SongsDir/.stepstash-usage.sqlite, outside temporary cache.
	StatsPath       string
	Origins         map[string]string
	DownloadTimeout time.Duration
	MaxFileBytes    int64
	// MaxCacheBytes bounds retained videos across cache and library; zero is unlimited.
	MaxCacheBytes int64
	MaxDownloads  int
	Logger        *slog.Logger
	// DialContext optionally supplies independent upstream DNS resolution.
	DialContext func(context.Context, string, string) (net.Conn, error)
}

func DefaultConfig() Config {
	return Config{
		CacheDir:        "stepstash-cache",
		SongsDir:        "wannadance-song",
		Origins:         map[string]string{"play.udon.dance": "ud-play.kiva.moe:80", "nya.xin.moe": "ud-nya.kiva.moe:80"},
		DownloadTimeout: 10 * time.Minute,
		MaxFileBytes:    2 << 30,
		MaxDownloads:    3,
		Logger:          slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
}

func (c Config) validate() error {
	if c.MaxCacheBytes < 0 {
		return errors.New("cache limit cannot be negative")
	}
	if c.CacheDir == "" || c.SongsDir == "" || c.DownloadTimeout <= 0 || c.MaxFileBytes <= 0 || c.MaxFileBytes == int64(^uint64(0)>>1) || c.MaxDownloads < 1 {
		return errors.New("cache directory, timeout, file limit and download limit must be positive")
	}
	for _, host := range []string{"play.udon.dance", "nya.xin.moe"} {
		addr, port, err := net.SplitHostPort(c.Origins[host])
		p, perr := strconv.Atoi(port)
		if err != nil || addr == "" || perr != nil || p < 1 || p > 65535 || addr == host {
			return errors.New("each video host needs a separate upstream host:port")
		}
	}
	return nil
}

func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
	}
}
