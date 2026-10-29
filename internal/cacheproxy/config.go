package cacheproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/videometa"
)

// Config controls the local video service. Origins are dial addresses, not URLs;
// by default, valid API-returned hosts are connected through the selected dialer.
// Overrides retain the original HTTP Host and TLS identity; environment proxies
// are not used.
type Config struct {
	// Optional observation sinks. They must be fast, concurrency-safe, and must
	// not call back into the engine or scheduler. Nil leaves normal work unchanged.
	ObserveBlockSchedule func(blocksched.Event)
	ObserveDownloadBytes func(int64)
	// BeginResourceLoad marks real upstream video work, including prefetch.
	// Release is called after the response closes; local hits never call it.
	BeginResourceLoad func() func()
	// BeginVideoRequest captures an observation window at HTTP arrival. The
	// returned callback runs only after validating a GET/HEAD video or playback
	// API request, before upstream resolution; it does not imply success.
	// Prefetch and HTTPS relay never call it.
	BeginVideoRequest func(time.Time) func()
	Store             *Store         // Application-owned; the engine never closes a supplied store.
	Work              *WorkDirectory // Application-owned block workspace, prepared before startup logging.
	VideoDir          string
	Origins           map[string]string
	// IsResourceHost checks the current API-derived resource domain set.
	IsResourceHost func(string) bool
	// OriginScheme defaults to HTTPS, with bounded HTTP fallback on transport
	// failures. Explicit HTTP remains restricted to loopback test origins.
	OriginScheme string
	// ResolvePlayback supplies a fresh address under the shared source policy.
	// It owns network selection and evidence recording, never download execution.
	// Nil uses the standalone resolver under the same source policy.
	ResolvePlayback func(context.Context, string) (string, error)
	// DownloadTimeout bounds each upstream attempt (headers and body), including
	// a sequential response, and each local validation/publication phase.
	// Scheduling waits are governed by demand leases and caller cancellation.
	DownloadTimeout time.Duration
	// BlockSize is the download/storage block size in bytes; zero uses
	// DefaultBlockSize. It is fixed at construction, including for old readers.
	// The caller owns buffer sizing; user-facing tuning limits are not defined here.
	BlockSize int64
	// PrepareAhead is the full lead M for scheduled downloads; zero uses the default.
	PrepareAhead time.Duration
	// DownloadSoftLimit bounds ordinary block execution; blocked reads may exceed it.
	DownloadSoftLimit int
	// DownloadGrowInterval paces ordinary capacity growth; zero disables pacing.
	DownloadGrowInterval time.Duration
	// LocalProtectAhead independently controls protection of finite usage.
	// Zero follows PrepareAhead; it does not grant untimed demands protection.
	// Normal configurations should cover the effective PrepareAhead; this design
	// convention is not enforced by validation.
	LocalProtectAhead time.Duration
	// LocalProtectionGrace retains already protected blocks after their final
	// usage ends; zero disables this tail without changing live protection.
	LocalProtectionGrace time.Duration
	MaxFileBytes         int64
	// CacheUnlimited disables the retained-video budget independently of its value.
	CacheUnlimited bool
	// MaxCacheBytes bounds ordinary retained videos. Favorites count toward usage
	// but can exceed this budget; zero reclaims unprotected ordinary videos.
	MaxCacheBytes int64
	// RetainFavorites allows favorite resources to exceed the video budget.
	// It affects retention only, never creates download demand.
	RetainFavorites bool
	// AggressiveCacheReclamation controls unprotected block cache reclamation:
	// auto follows the video budget; on and off explicitly override it.
	AggressiveCacheReclamation string
	// RequestRetentionDays bounds request details; zero disables detail retention.
	// Unlimited retention is explicit and takes precedence over the day limit.
	RequestRetentionDays      int
	RequestRetentionUnlimited bool
	// MaxLocalValidations bounds concurrent local file validations.
	// Usage capacity and upstream block execution have independent limits.
	MaxLocalValidations int
	Logger              *slog.Logger
	// DialContext supplies upstream connections (independent DNS or SOCKS5).
	DialContext func(context.Context, string, string) (net.Conn, error)
	// ResourceTransports supplies ranked or cold-start channels for this URL's host,
	// bounded by upstreamrequest.MaxOperationCandidates before download begins.
	// It never resolves a song or changes the resource address/content.
	ResourceTransports func(string) []http.RoundTripper
}

func DefaultConfig() Config {
	return Config{
		CacheUnlimited:             false,
		MaxCacheBytes:              0,
		RetainFavorites:            true,
		AggressiveCacheReclamation: "auto",
		RequestRetentionUnlimited:  true,
		OriginScheme:               "https",
		Origins:                    make(map[string]string),
		DownloadTimeout:            10 * time.Minute,
		BlockSize:                  DefaultBlockSize,
		PrepareAhead:               blocksched.DefaultPrepareAhead,
		DownloadSoftLimit:          VideoRequestSoftLimit,
		DownloadGrowInterval:       DefaultDownloadGrowInterval,
		LocalProtectionGrace:       DefaultLocalProtectionGrace,
		MaxFileBytes:               2 << 30,
		MaxLocalValidations:        3,
		Logger:                     slog.Default(),
	}
}

func (c Config) validate() error {
	// Each executing Range may allocate BlockSize+1 bytes (the extra byte
	// detects oversized responses). Reject sizes that cannot fit a Go buffer.
	if c.BlockSize < 0 || uint64(c.BlockSize) >= uint64(^uint(0)>>1) {
		return errors.New("block size must be nonnegative and fit a buffer with one extra byte")
	}
	if c.AggressiveCacheReclamation != "auto" && c.AggressiveCacheReclamation != "on" && c.AggressiveCacheReclamation != "off" {
		return errors.New("aggressive cache reclamation must be auto, on or off")
	}
	if c.DownloadSoftLimit < 1 || c.DownloadSoftLimit > MaxDownloadSoftLimit {
		return errors.New("download soft limit must be between 1 and 64")
	}
	if c.DownloadGrowInterval < 0 || c.DownloadGrowInterval > MaxDownloadGrowInterval {
		return errors.New("download growth interval must be between 0 and 2000 milliseconds")
	}
	if c.LocalProtectAhead < 0 || c.LocalProtectAhead > 24*time.Hour {
		return errors.New("local protection lead must be between 0 and 86400 seconds")
	}
	if c.LocalProtectionGrace < 0 || c.LocalProtectionGrace > MaxLocalProtectionGrace {
		return errors.New("local protection grace must be between 0 and 10000 seconds")
	}
	if c.OriginScheme != "https" && c.OriginScheme != "http" {
		return errors.New("invalid origin scheme")
	}
	if c.RequestRetentionDays < 0 || c.RequestRetentionDays > 36500 {
		return errors.New("request retention must be between 0 and 36500 days")
	}
	if c.MaxCacheBytes < 0 {
		return errors.New("video storage budget cannot be negative")
	}
	if c.PrepareAhead != 0 && (c.PrepareAhead < blocksched.MinPrepareAhead || c.PrepareAhead > blocksched.MaxPrepareAhead) {
		return errors.New("download preparation lead must be between 100 and 16000 seconds")
	}
	if c.VideoDir == "" || c.DownloadTimeout <= 0 || c.MaxFileBytes <= 0 || c.MaxFileBytes == int64(^uint64(0)>>1) || c.MaxLocalValidations < 1 {
		return errors.New("video directory is required; timeout, file limit and download limit must be positive")
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
