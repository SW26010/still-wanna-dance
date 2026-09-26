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
	songID                           string
	checksum, key, path, query, host string
	size                             int64
}
type flight struct {
	id           uint64
	log          *slog.Logger
	progress     *downloadProgress
	done         chan struct{}
	streaming    chan struct{}
	spool        *spool
	path, source string
	err          error
}

type Server struct {
	stats           trafficStats
	routeMu         sync.Mutex
	routeSongs      map[string]string
	routeCache      map[string]routeEntry
	routeHealth     map[string]routeHealth
	routeProbes     map[string]chan struct{}
	cfg             Config
	client          *http.Client
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	flights         map[string]*flight
	slots           chan struct{}
	background      int
	capacityChanged chan struct{}
	wg              sync.WaitGroup
	closed          bool
	unlock          func() error
	once            sync.Once
	sequence        atomic.Uint64
	flightSequence  atomic.Uint64
	usage           *usageStore
	retentionMu     sync.Mutex
	currentMu       sync.Mutex
	currentLocks    map[string]*songConfirmation
	versionPins     map[string]int
	cleanupNeeded   map[string]bool
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
	if cfg.DialContext != nil {
		transport.DialContext = cfg.DialContext
	}

	usage, usageErr := openUsage(filepath.Join(cfg.StorageDir, "stepstash.sqlite"), cfg.Logger)
	if usageErr != nil {
		cancel()
		unlock()
		return nil, fmt.Errorf("open storage database: %w", usageErr)
	}
	s := &Server{cfg: cfg, ctx: ctx, cancel: cancel, unlock: unlock,
		versionPins: make(map[string]int), cleanupNeeded: make(map[string]bool),
		usage:   usage,
		flights: make(map[string]*flight), slots: make(chan struct{}, cfg.MaxDownloads), capacityChanged: make(chan struct{}),
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	s.cleanSupersededOnStartup()
	s.trimCache()
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
		s.usage.close()
		s.client.CloseIdleConnections()
		err = s.unlock()
		s.cfg.Logger.Info("cache_engine_stopped", "error", err)
	})
	return err
}

func (s *Server) parse(r *http.Request) (video, error) {
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
	if err != nil || size <= 0 || size > s.cfg.MaxFileBytes {
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
	v, err := s.parse(r)
	if err != nil {
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
	f, stream, err := s.obtain(r.Context(), v)
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
		log.Info("served", "cache", "MISS", "elapsed", time.Since(start))
		return
	}
	file, err := os.Open(f.path)
	if err != nil {
		log.Error("open_failed", "error", err)
		http.Error(w, "cache unavailable", 500)
		return
	}
	defer file.Close()
	// Opened handles pin the version even if another request replaces the library
	// entry. Validate this handle to close the prepare/open publication race.
	if err := checkOpenFile(r.Context(), file, v); err != nil {
		log.Warn("file_changed", "error", err)
		http.Error(w, "file changed; retry request", http.StatusServiceUnavailable)
		return
	}
	if _, err := file.Seek(0, 0); err != nil {
		http.Error(w, "cache unavailable", 500)
		return
	}
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "cache unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", `"`+v.checksum+`"`)
	w.Header().Set("X-StepStash-Cache", f.source)
	http.ServeContent(w, r, v.path, info.ModTime(), file)
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
