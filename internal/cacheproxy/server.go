package cacheproxy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/videometa"
)

type video struct {
	cached                 *SongURL
	refresh                *playbackCheck
	localOnly              bool
	songID                 string
	key, path, query, host string
	size                   int64
}
type flight struct {
	size          int64
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
	resourceHosts    sync.Map // validated API/programmatic URLs, host -> expiry
	mappingMu        sync.Mutex
	verifications    *verificationStore
	stats            trafficStats
	cfg              Config
	client           *http.Client
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	storageFailure   error // protected by mu; prevents new downloads until restart
	flights          map[string]*flight
	playbackChecks   map[string]*playbackCheck // protected by mu
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
	versionPins      map[string]int
	songResources    map[string]map[string]bool
	queueSongs       map[string]bool
	queueProtected   map[string]bool
	queueHandoffs    map[string]time.Time
	handoffProtected map[string]time.Time
	deletingVideos   map[string]chan struct{}
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
	if err := checkStorageFormat(root); err != nil {
		return nil, err
	}
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
		if cfg.OriginScheme == "http" {
			ipHost, _, _ := net.SplitHostPort(address)
			ip := net.ParseIP(ipHost)
			if ip == nil || !ip.IsLoopback() {
				return nil, errors.New("HTTP origins must be loopback")
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
		versionPins: make(map[string]int),
		usage:       usage,
		flights:     make(map[string]*flight), slots: make(chan struct{}, cfg.MaxDownloads), localChecks: make(chan struct{}, cfg.MaxDownloads), capacityChanged: make(chan struct{}),
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
		s.cfg.Logger.Info("cache_engine_stopped", "error", applog.SafeError(err))
	})
	return err
}

func (s *Server) parse(r *http.Request) (video, error) {
	return parseVideo(r, s.cfg.MaxFileBytes)
}

func (s *Server) resourceHostAllowed(host string) bool {
	if _, explicit := s.cfg.Origins[host]; explicit {
		return true
	}
	if s.cfg.IsResourceHost != nil {
		return s.cfg.IsResourceHost(host)
	}
	until, ok := s.resourceHosts.Load(host)
	return ok && time.Now().Before(until.(time.Time))
}

// Resolver results and explicit programmatic prefetch targets establish
// membership. Untrusted inbound requests never call this method.
func (s *Server) parseResolved(r *http.Request) (video, error) {
	if err := ValidateVideoURL(r.URL.String(), s.cfg.MaxFileBytes); err != nil {
		return video{}, err
	}
	v, err := s.parse(r)
	if err == nil {
		s.resourceHosts.Store(v.host, time.Now().Add(10*time.Minute))
	}
	return v, err
}

// ValidateVideoURL applies the cache parser's rules before a resolver accepts a route.
func ValidateVideoURL(target string, maxFileBytes int64) error {
	r, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return errors.New("invalid video URL")
	}
	if (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.User != nil || r.URL.Fragment != "" || !videometa.ValidHost(r.URL.Host) {
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
	if !videometa.ValidHost(host) {
		return v, errors.New("unsupported host")
	}
	meta, err := videometa.Parse(r.URL, maxFileBytes)
	if err != nil {
		return v, err
	}
	return video{size: meta.Size, key: meta.Checksum, path: r.URL.Path, query: r.URL.RawQuery, host: host}, nil
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
	var observed func()
	if s.cfg.BeginVideoRequest != nil {
		observed = s.cfg.BeginVideoRequest(start)
	}
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
			"response_headers_ms", response.headerLatency.Milliseconds(), "first_body_ms", response.firstBodyMS(),
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
	v, release, err := s.requestVideo(r, observed)
	if release != nil {
		defer release()
	}
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
		songID: v.songID,
		source: "http", method: r.Method, rangeHeader: r.Header.Get("Range"), size: v.size}
	defer func() {
		panicked := recover()
		event.status, event.bytes = response.status, response.bytes
		event.firstBodyNS = response.firstBodyLatency.Nanoseconds()
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
	event.demand = r.Method == http.MethodGet && v.songID != ""
	s.pinVideo(v)
	defer s.releaseVideo(v)
	if r.Method == http.MethodGet {
		event.at = s.usage.startGET(v.songID, v.key)
	}
	var f *flight
	var stream *spoolReader
loadVideo:
	if v.localOnly {
		// Do not enter obtain: it can download or update the current song mapping.
		_, err = s.verifiedFile(r.Context(), v)
		f = &flight{source: "HIT"}
		if err == nil {
			log.Info("playback_local_hit", "song_id", v.songID)
		}
	} else {
		f, stream, err = s.obtain(r.Context(), v)
		if err != nil && v.cached != nil && errors.Is(err, errUpstreamDownload) && r.Context().Err() == nil {
			if replacement, fallbackErr := s.retrySongURL(r.Context(), v); fallbackErr == nil {
				v = replacement
				s.pinVideo(v)
				defer s.releaseVideo(v)
				event.id, event.key, event.host, event.size = v.key, v.key, v.host, v.size
				f, stream, err = s.obtain(r.Context(), v)
			} else {
				err = errors.Join(err, fmt.Errorf("cached URL refresh: %w", fallbackErr))
			}
		}
	}
	if err != nil {
		if r.Context().Err() != nil {
			log.Info("client_disconnected", "elapsed", time.Since(start))
			return
		}
		log.Error("cache_failed", "error", applog.SafeError(err), "elapsed", time.Since(start))
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
		pending := &streamResponseWriter{ResponseWriter: w, header: w.Header().Clone(), stream: stream}
		pending.Header().Set("Content-Type", "video/mp4")
		pending.Header().Set("ETag", `"`+v.key+`"`)
		pending.Header().Set("X-StepStash-Cache", "MISS")
		http.ServeContent(pending, r, v.path, time.Time{}, stream)
		if stream.err != nil {
			if v.cached != nil && errors.Is(stream.err, errUpstreamDownload) && r.Context().Err() == nil {
				if !pending.committed {
					// finish wakes readers before the failed flight is removed.
					// Wait for removal so a same-MD5 retry starts a new flight.
					select {
					case <-f.done:
					case <-r.Context().Done():
						return
					}
					if replacement, fallbackErr := s.retrySongURL(r.Context(), v); fallbackErr == nil {
						v = replacement
						s.pinVideo(v)
						defer s.releaseVideo(v)
						event.id, event.key, event.host, event.size = v.key, v.key, v.host, v.size
						goto loadVideo
					} else {
						stream.err = errors.Join(stream.err, fmt.Errorf("cached URL refresh: %w", fallbackErr))
					}
				}
				if rejectErr := s.RejectSongURL(r.Context(), *v.cached); rejectErr != nil {
					log.Warn("song_url_reject_failed", "error", applog.SafeError(rejectErr))
				}
			}
			if r.Context().Err() != nil {
				log.Info("client_disconnected", "cache", "MISS")
			} else {
				log.Error("stream_failed", "error", applog.SafeError(stream.err))
			}
			if !pending.committed {
				status := http.StatusBadGateway
				if errors.Is(stream.err, context.DeadlineExceeded) {
					status = http.StatusGatewayTimeout
				}
				if errors.Is(stream.err, context.Canceled) {
					status = http.StatusServiceUnavailable
				}
				http.Error(w, http.StatusText(status), status)
				return
			}
			// Headers may already be sent. Abort rather than completing a truncated body.
			panic(http.ErrAbortHandler)
		}
		pending.commit()
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
		log.Error("open_failed", "error", applog.SafeError(err))
		if errors.Is(err, errInvalidCache) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "file changed or validation interrupted; retry request", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "cache unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", `"`+v.key+`"`)
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
	started          time.Time
	headerLatency    time.Duration
	firstBodyLatency time.Duration
	status           int
	bytes            int64
	writeErr         error
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
	if n > 0 && w.bytes == 0 {
		w.firstBodyLatency = time.Since(w.started)
	}
	w.bytes += int64(n)
	if err != nil {
		w.writeErr = err
	}
	return n, err
}

func (w *responseWriter) firstBodyMS() any {
	if w.bytes == 0 {
		return nil
	}
	return float64(w.firstBodyLatency) / float64(time.Millisecond)
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeContent chooses the range and status, but headers stay private until
// the first readable bytes. This leaves failed pre-body streams recoverable.
type streamResponseWriter struct {
	http.ResponseWriter
	header    http.Header
	stream    *spoolReader
	status    int
	committed bool
}

func (w *streamResponseWriter) Header() http.Header { return w.header }
func (w *streamResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	// If-Range can turn a ranged request into a full response. Only the
	// actual response status determines whether to hold the final byte.
	w.stream.verifyFull = status == http.StatusOK
}
func (w *streamResponseWriter) commit() {
	if w.committed {
		return
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	for key, values := range w.header {
		w.ResponseWriter.Header()[key] = values
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.committed = true
}
func (w *streamResponseWriter) Write(p []byte) (int, error) {
	w.commit()
	n, err := w.ResponseWriter.Write(p)
	if err == nil {
		http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}
