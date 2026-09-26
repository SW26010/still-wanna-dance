package cacheproxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stepstash/internal/applog"
)

var videoPath = regexp.MustCompile(`^/files/[0-9]+/([1-9][0-9]*)-([a-zA-Z0-9]+)\.mp4$`)

type video struct {
	localOnly                        bool
	preferRequestedRoute             bool
	songID                           string
	checksum, key, path, query, host string
	size                             int64
}
type flight struct {
	monitorSongs  map[string]bool // protected by Server.mu; includes background callers
	playbackSongs map[string]*playbackSong
	id            uint64
	log           *slog.Logger
	progress      *downloadProgress
	done          chan struct{}
	streaming     chan struct{}
	spool         *spool
	path, source  string
	err           error
}

type Server struct {
	verifications    *verificationStore
	stats            trafficStats
	routeMu          sync.Mutex
	routeSongs       routeLRU[string]
	routeCache       routeLRU[[]string]
	routeHealth      map[string]routeHealth
	routeProbes      map[string]chan struct{}
	cfg              Config
	client           *http.Client
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	flights          map[string]*flight
	slots            chan struct{}
	localChecks      chan struct{}
	verifyMu         sync.Mutex
	verified         map[string]*verifiedFile
	background       int
	capacityChanged  chan struct{}
	wg               sync.WaitGroup
	closed           bool
	unlock           func() error
	once             sync.Once
	sequence         atomic.Uint64
	flightSequence   atomic.Uint64
	usage            *usageStore
	retentionMu      sync.Mutex
	retentionRunMu   sync.Mutex
	retentionWake    chan struct{}
	retentionDone    chan struct{}
	retained         map[string]retainedVideo
	retainedBytes    int64
	retentionChanges map[string]bool
	currentMu        sync.Mutex
	currentLocks     map[string]*songConfirmation
	versionPins      map[string]int
	songResources    map[string]map[string]bool
	queueSongs       map[string]bool
	queueProtected   map[string]bool
	queueHandoffs    map[string]time.Time
	handoffProtected map[string]time.Time
	cleanupNeeded    map[string]bool
	deletingVideos   map[string]chan struct{}
	mappingRevision  uint64 // protected by retentionMu; invalidates cleanup queries

}

func New(cfg Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cfg.StorageDir)
	if err != nil {
		return nil, err
	}
	cfg.StorageDir = root
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.Logger = cfg.Logger.With("service_id", rand.Text())
	origins := make(map[string]string, len(cfg.Origins))
	for host, addr := range cfg.Origins {
		origins[host] = addr
	}
	cfg.Origins = origins
	if err := os.MkdirAll(cfg.StorageDir, 0700); err != nil {
		return nil, err
	}
	unlock, err := lockDirectory(filepath.Join(cfg.StorageDir, ".lock"))
	if err != nil {
		return nil, fmt.Errorf("lock cache directory (another process may own it): %w", err)
	}
	for _, dir := range []string{cfg.videosDir(), cfg.tempDir()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			unlock()
			return nil, err
		}
	}
	// The OS lock is released after a crash. Only our own partial files are removed.
	entries, err := os.ReadDir(cfg.tempDir())
	cleaned := 0
	if err == nil {
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "download-") || !strings.HasSuffix(entry.Name(), ".part") || entry.IsDir() {
				continue
			}
			if err = os.Remove(filepath.Join(cfg.tempDir(), entry.Name())); err != nil {
				break
			}
			cleaned++
		}
	}
	if err != nil {
		unlock()
		return nil, fmt.Errorf("clean partial downloads: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := newTransport()
	dial := transport.DialContext
	if cfg.DialContext != nil {
		dial = cfg.DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if origin, ok := cfg.Origins[host]; ok {
			address = origin
			if cfg.OriginScheme == "http" {
				ipHost, _, _ := net.SplitHostPort(origin)
				ip := net.ParseIP(ipHost)
				if ip == nil || !ip.IsLoopback() {
					return nil, errors.New("HTTP origins must be loopback")
				}
			}
		}
		return dial(ctx, network, address)
	}

	usage, usageErr := openUsage(filepath.Join(cfg.StorageDir, "stepstash.sqlite"), cfg.Logger, cfg.RequestRetentionDays)
	if usageErr != nil {
		cancel()
		unlock()
		return nil, fmt.Errorf("open storage database: %w", usageErr)
	}
	s := &Server{cfg: cfg, ctx: ctx, cancel: cancel, unlock: unlock,
		versionPins: make(map[string]int), cleanupNeeded: make(map[string]bool),
		usage:   usage,
		flights: make(map[string]*flight), slots: make(chan struct{}, cfg.MaxDownloads), localChecks: make(chan struct{}, cfg.MaxDownloads), capacityChanged: make(chan struct{}),
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if err := s.loadTraffic(usage.db); err != nil {
		cancel()
		usage.close()
		unlock()
		return nil, fmt.Errorf("load traffic statistics: %w", err)
	}
	if err := s.loadSongResources(); err != nil {
		cancel()
		usage.close()
		unlock()
		return nil, fmt.Errorf("load song resources: %w", err)
	}
	s.verifications, err = openVerificationStore(cfg.StorageDir)
	if err != nil {
		cancel()
		usage.close()
		unlock()
		return nil, fmt.Errorf("open verification database: %w", err)
	}
	s.cleanSupersededOnStartup()
	s.trimCache()
	s.retentionWake = make(chan struct{}, 1)
	s.retentionDone = make(chan struct{})
	go s.retentionLoop()
	cfg.Logger.Info("cache_engine_ready", "removed_partials", cleaned, "max_downloads", cfg.MaxDownloads,
		"download_timeout_ms", cfg.DownloadTimeout.Milliseconds(), "max_cache_bytes", cfg.MaxCacheBytes)
	return s, nil
}

// Close cancels downloads, waits for handlers and cleanup, then flushes usage.
// The owner must shut down/close its HTTP server first to release blocked writes.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		<-s.retentionDone
		// All references are now released; finish any deferred eviction before closing usage.
		s.ResetQueueSongs(nil)
		s.runRetention(false)
		s.verifications.db.Close()
		s.usage.close()
		s.client.CloseIdleConnections()
		err = s.unlock()
		s.cfg.Logger.Info("cache_engine_stopped", "error", err)
	})
	return err
}

func (s *Server) parse(r *http.Request) (video, error) {
	return parseVideo(r, s.cfg.MaxFileBytes)
}

// ValidateVideoURL applies the cache parser's rules before a resolver accepts a route.
func ValidateVideoURL(target string, maxFileBytes int64) error {
	r, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return errors.New("invalid video URL")
	}
	if (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.User != nil || r.URL.Fragment != "" {
		return errors.New("unsupported video URL")
	}
	_, err = parseVideo(r, maxFileBytes)
	return err
}

func parseVideo(r *http.Request, maxFileBytes int64) (video, error) {
	var v video
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "play.udon.dance" && host != "nya.xin.moe" {
		return v, errors.New("unsupported host")
	}
	m := videoPath.FindStringSubmatch(r.URL.Path)
	if m == nil || r.URL.RawPath != "" {
		return v, errors.New("invalid video path")
	}
	q, err := urlQuery(r)
	if err != nil {
		return v, err
	}
	checksum := strings.ToLower(q[0])
	digest, err := hex.DecodeString(checksum)
	if err != nil || len(digest) != 16 {
		return v, errors.New("e must be a 32-character MD5")
	}
	size, err := strconv.ParseInt(q[1], 10, 64)
	if err != nil || size <= 0 || size > maxFileBytes {
		return v, errors.New("s exceeds allowed size or is invalid")
	}
	key := sha256.Sum256([]byte(m[1] + "/" + m[2] + "/" + checksum + "/" + strconv.FormatInt(size, 10)))
	return video{checksum: checksum, size: size, key: hex.EncodeToString(key[:]), path: r.URL.Path, query: r.URL.RawQuery, host: host}, nil
}

func urlQuery(r *http.Request) ([2]string, error) {
	var values [2]string
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return values, errors.New("invalid query")
	}
	for i, name := range []string{"e", "s"} {
		if len(q[name]) != 1 {
			return values, errors.New("exactly one e and s required")
		}
		values[i] = q[name][0]
	}
	return values, nil
}

// Register before Close starts waiting so completed observations drain to disk.
func (s *Server) beginRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.beginRequest() {
		http.Error(w, "service closed", 503)
		return
	}
	defer s.wg.Done()
	id := s.sequence.Add(1)
	start := time.Now()
	r = r.WithContext(applog.WithTrace(r.Context()))
	log := s.cfg.Logger.With("trace_id", applog.TraceID(r.Context()), "request_id", id, "method", r.Method, "host", r.Host, "path", r.URL.Path, "range", r.Header.Get("Range"), "user_agent", r.UserAgent())
	response := &responseWriter{ResponseWriter: w, started: start}
	w = response
	defer func() {
		crash := recover()
		outcome := "completed"
		if response.status >= 400 || response.writeErr != nil {
			outcome = "failed"
		}
		if crash != nil {
			outcome = "aborted"
		}
		if r.Context().Err() != nil {
			outcome = "canceled"
		}
		log.Info("request_finished", "status", response.status, "bytes", response.bytes, "elapsed", time.Since(start), "elapsed_ms", time.Since(start).Milliseconds(),
			"outcome", outcome, "cache", w.Header().Get("X-StepStash-Cache"), "content_range", w.Header().Get("Content-Range"),
			"content_length", w.Header().Get("Content-Length"), "write_error", applog.SafeError(response.writeErr))
		if crash != nil {
			panic(crash)
		}
	}()
	w.Header().Set("X-Request-ID", strconv.FormatUint(id, 10))
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	// Non-playback API endpoints retain their upstream behavior through the
	// encrypted relay; only the playback endpoint returns local video bytes.
	apiHost := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(apiHost); err == nil {
		apiHost = h
	}
	if apiHost == "api.udon.dance" && r.URL.Path != "/Api/Songs/play" {
		u := &url.URL{Scheme: "https", Host: "api.udon.dance", Path: r.URL.Path, RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
		return
	}
	v, err := s.requestVideo(r)
	if err != nil {
		if errors.Is(err, errPlaybackUpstream) {
			http.Error(w, "playback resolution failed", http.StatusBadGateway)
			return
		}
		http.Error(w, err.Error(), 400)
		return
	}
	log = log.With("resource_key", v.key)
	log.Info("request_started")
	event := usageEvent{id: v.key, at: start.UnixMilli(), key: v.key, host: v.host,
		source: "http", method: r.Method, rangeHeader: r.Header.Get("Range"), size: v.size}
	defer func() {
		panicked := recover()
		event.status, event.bytes = response.status, response.bytes
		event.elapsedMS = time.Since(start).Milliseconds()
		event.cache = w.Header().Get("X-StepStash-Cache")
		if event.cache == "" {
			event.cache = "UNKNOWN"
		}
		event.outcome = "completed"
		if event.status >= 400 || response.writeErr != nil {
			event.outcome = "failed"
		}
		if panicked != nil {
			event.outcome = "aborted"
		}
		if r.Context().Err() != nil {
			event.outcome = "canceled"
		}
		s.usage.record(event)
		s.recordTraffic(r.Method, event.cache, event.outcome, event.status, response.headerLatency, response.bytes)
		if panicked != nil {
			panic(panicked)
		}
	}()
	// Range only applies to GET. ServeContent also handles HEAD, so strip it here.
	if r.Method == http.MethodHead {
		r = r.Clone(r.Context())
		r.Header.Del("Range")
		r.Header.Del("If-Range")
	}
	// Avoid unbounded multipart response amplification. Single ranges use net/http semantics.
	if strings.Contains(r.Header.Get("Range"), ",") {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", v.size))
		http.Error(w, "multiple ranges are not supported", 416)
		return
	}
	event.demand = r.Method == http.MethodGet
	s.pinVideo(v)
	defer s.releaseVideo(v)
	if event.demand {
		event.at = s.usage.startDemand(v.key)
	}
	var f *flight
	var stream *spoolReader
	if v.localOnly {
		// Do not enter obtain: it can download or update the current song mapping.
		_, err = s.verifiedFile(r.Context(), v)
		f = &flight{source: "HIT"}
		if err == nil {
			w.Header().Set("X-StepStash-Fallback", "upstream-unavailable")
			log.Warn("playback_local_fallback", "song_id", v.songID)
		}
	} else {
		f, stream, err = s.obtain(r.Context(), v)
	}
	if err != nil {
		if r.Context().Err() != nil {
			log.Info("client_disconnected", "elapsed", time.Since(start))
			return
		}
		log.Error("cache_failed", "error", err, "elapsed", time.Since(start))
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		if errors.Is(err, errBusy) || errors.Is(err, context.Canceled) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	if stream != nil {
		defer stream.Close()
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("ETag", `"`+v.checksum+`"`)
		w.Header().Set("X-StepStash-Cache", "MISS")
		http.ServeContent(flushingResponseWriter{w}, r, v.path, time.Time{}, stream)
		if stream.err != nil {
			if r.Context().Err() != nil {
				log.Info("client_disconnected", "cache", "MISS")
			} else {
				log.Error("stream_failed", "error", stream.err)
			}
			// Headers may already be sent. Abort rather than completing a truncated body.
			panic(http.ErrAbortHandler)
		}
		// A complete GET has finished validation/publication. Drain its song
		// record too; short Range/HEAD responses leave this to the worker.
		if r.Method == http.MethodGet && response.status == http.StatusOK {
			if err := s.waitPlaybackSong(r.Context(), f, v.songID); err != nil {
				log.Error("playback_song_failed", "error", applog.SafeError(err))
			}
		}
		log.Info("served", "cache", "MISS", "elapsed", time.Since(start))
		return
	}
	file, err := s.verifiedFile(r.Context(), v)
	if err != nil {
		log.Error("open_failed", "error", err)
		if errors.Is(err, errInvalidCache) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "file changed or validation interrupted; retry request", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "cache unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", `"`+v.checksum+`"`)
	w.Header().Set("X-StepStash-Cache", f.source)
	http.ServeContent(w, r, v.path, file.info.ModTime(), file.reader())
	if r.Context().Err() != nil {
		log.Info("client_disconnected", "cache", f.source, "elapsed", time.Since(start))
		return
	}
	log.Info("served", "cache", f.source, "elapsed", time.Since(start))
}

type responseWriter struct {
	http.ResponseWriter
	started       time.Time
	headerLatency time.Duration
	status        int
	bytes         int64
	writeErr      error
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.headerLatency = time.Since(w.started)
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	if err != nil {
		w.writeErr = err
	}
	return n, err
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush headers and small prefixes promptly; URL resolvers may read only a few
// bytes before disconnecting, while the shared background download continues.
type flushingResponseWriter struct{ http.ResponseWriter }

func (w flushingResponseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	http.NewResponseController(w.ResponseWriter).Flush()
}
func (w flushingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err == nil {
		http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}
