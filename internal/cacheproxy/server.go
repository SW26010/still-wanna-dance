package cacheproxy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/videometa"
	"still-wanna-dance/internal/workfiles"
)

type video struct {
	candidates             *songCandidates
	cached                 *SongURL
	refresh                *playbackCheck
	localOnly              bool
	songID                 string
	key, path, query, host string
	size                   int64
}
type flight struct {
	discard          bool                    // isolated measurement only; never serves cached media
	cancel           context.CancelCauseFunc // protected by Server.mu
	preempted        bool                    // protected by Server.mu; no new readers once cleanup is committed
	completed        bool                    // protected by Server.mu; published/hit before reader wakeup
	sequentialCtx    context.Context         // protected by Server.mu; canceled sequential bodies cannot be shared
	background       bool
	size             int64
	monitorSongs     map[string]bool // protected by Server.mu; includes background callers
	releases         []func()        // attached obtain callers; invoked after flight cleanup
	sequentialDemand func(int64) func()
	playbackSongs    map[string]*playbackSong
	id               uint64
	log              *slog.Logger
	progress         *downloadProgress
	done             chan struct{}
	streaming        chan struct{}
	spool            *spool
	ranges           *localResource // execution reference to engine-owned complete blocks
	path, source     string
	err              error
}

type Server struct {
	*Store
	getMu            sync.Mutex // Current incoming GET observations; independent of worker locks.
	currentGETs      map[uint64]*currentGET
	resourceHosts    sync.Map // validated API/programmatic URLs, host -> expiry
	stats            trafficStats
	cfg              Config
	client           *http.Client
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	storageFailure   error // protected by mu; prevents new downloads until restart
	flights          map[string]*flight
	resourceUses     map[string]*resourceUse  // protected by mu; completed Range work with continuing usage
	demands          map[*downloadDemand]bool // protected by mu, separate from file pins
	useBlocks        map[useBlockID]int       // protected by mu; includes readable blocks
	useHandoffs      useHandoffHeap
	useTimer         *time.Timer // Handoff expiry and idle queue clock refresh.
	prefetchSources  map[string]*queueSource
	prefetchOrder    []int64
	queueStart       time.Time
	playbackChecks   map[string]*playbackCheck // protected by mu
	localChecks      chan struct{}
	requests         *requestActivity
	scheduler        *blocksched.Scheduler
	blockObserver    atomic.Pointer[blockObserver] // allocated only while a visualization is subscribed
	rangeBuffers     chan []byte
	verifyMu         sync.Mutex
	verified         map[string]*verifiedFile
	background       int
	capacityChanged  chan struct{}
	resourceChanges  map[string]*resourceChange // protected by mu; only subscribed resources
	wg               sync.WaitGroup
	closed           bool
	unlock           func() error
	once             sync.Once
	sequence         atomic.Uint64
	flightSequence   atomic.Uint64
	retentionMu      sync.Mutex
	protection       map[string]map[int64]blockProtection // guarded by retentionMu
	publications     map[string]*publicationReservation   // guarded by retentionMu; one per shared flight
	localResources   map[string]*localResource            // guarded by retentionMu
	retentionWake    chan struct{}
	retentionDone    chan struct{}
	retained         map[string]retainedVideo
	retainedBytes    int64
	retentionChanges map[string]bool
	versionPins      map[string]int
	deletingVideos   map[string]chan struct{}

	queuePreparationChanged chan struct{} // protected by mu; lazily subscribed
}

func New(cfg Config) (*Server, error) {
	if cfg.BlockSize == 0 {
		cfg.BlockSize = DefaultBlockSize
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Store == nil || cfg.Work == nil {
		return nil, errors.New("视频引擎需要已打开的数据库和工作区")
	}
	paths, err := cfg.libraryPaths().Resolve()
	if err != nil {
		return nil, err
	}
	cfg.VideoDir = paths.VideoDir
	if pathWithin(cfg.Work.Path(), cfg.Store.path) || pathWithin(cfg.Work.Path(), cfg.VideoDir) || pathWithin(cfg.VideoDir, cfg.Work.Path()) {
		return nil, errors.New("块工作区不能与数据库或完整视频目录重叠")
	}
	if _, err := CacheDirectory(cfg.VideoDir); err != nil {
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
	store := cfg.Store
	unlock, err := lockVideoDirectory(cfg.VideoDir, store)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			unlock()
		}
	}()
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

	s := &Server{Store: store, cfg: cfg, ctx: ctx, cancel: cancel, unlock: unlock,
		requests:      new(requestActivity),
		rangeBuffers:  make(chan []byte, cfg.DownloadSoftLimit),
		versionPins:   make(map[string]int),
		retentionWake: make(chan struct{}, 1), retentionDone: make(chan struct{}),
		flights: make(map[string]*flight), localChecks: make(chan struct{}, cfg.MaxLocalValidations), capacityChanged: make(chan struct{}),
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if err := store.attach(s); err != nil {
		cancel()
		return nil, err
	}
	defer func() {
		if failed {
			cancel()
			s.client.CloseIdleConnections()
			store.detach(s)
		}
	}()
	// Claim the engine before touching media work owned by its video lease.
	if err := preparePublicationDirectory(cfg.VideoDir, cfg.publishDir()); err != nil {
		return nil, err
	}
	if err := workfiles.CheckDir(cfg.Work.Path()); err != nil {
		return nil, err
	}
	cleaned, err := cleanMediaWorkFiles(cfg.publishDir(), cfg.Logger, publicationWork)
	if err != nil {
		return nil, fmt.Errorf("clean temporary media files: %w", err)
	}
	if err := store.usage.start(cfg.RequestRetentionDays); err != nil {
		return nil, err
	}
	if err := s.loadTraffic(store.db); err != nil {
		return nil, fmt.Errorf("load traffic statistics: %w", err)
	}
	s.trimCache()
	go s.retentionLoop()
	s.scheduler = newDownloadScheduler(cfg)
	cfg.Logger.Info("cache_engine_ready", "removed_partials", cleaned, "max_local_validations", cfg.MaxLocalValidations,
		"block_size_bytes", cfg.BlockSize,
		"video_request_soft_limit", cfg.DownloadSoftLimit, "download_grow_interval_ms", cfg.DownloadGrowInterval.Milliseconds(),
		"download_timeout_ms", cfg.DownloadTimeout.Milliseconds(), "max_cache_bytes", cfg.MaxCacheBytes, "cache_unlimited", cfg.CacheUnlimited,
		"aggressive_cache_reclamation", cfg.AggressiveCacheReclamation)
	failed = false
	return s, nil
}

func newDownloadScheduler(cfg Config) *blocksched.Scheduler {
	return blocksched.New(blocksched.Config{SoftLimit: cfg.DownloadSoftLimit,
		SampleBytes:  cfg.BlockSize,
		GrowInterval: cfg.DownloadGrowInterval,
		PrepareAhead: cfg.PrepareAhead,
		Observe: func(e blocksched.Event) {
			if cfg.ObserveBlockSchedule != nil {
				cfg.ObserveBlockSchedule(e)
			}
			cfg.Logger.Debug("block_schedule", "event", e.Kind, "block_key", e.Key, "reason", e.Reason, "active", e.Active, "open", e.Open, "blocking", e.Blocking, "opportunistic", e.Opportunistic, "ready_by", e.ReadyBy, "work_bytes", e.WorkBytes, "block_time_ms", e.BlockTime.Milliseconds(), "prepare_ahead_ms", e.PrepareAhead.Milliseconds(), "overdue", e.Overdue)
		}})
}

// Close cancels downloads, waits for handlers and cleanup, then detaches from the store.
// The owner must shut down/close its HTTP server first to release blocked writes.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.useTimer != nil {
			s.useTimer.Stop()
		}
		s.notifyCapacityLocked()
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		s.scheduler.Close()
		s.closeLocalResources()
		<-s.retentionDone
		// All references are now released; finish any deferred eviction before closing usage.
		s.retentionMu.Lock()
		clear(s.protection)
		s.retentionMu.Unlock()
		s.runRetention(false)
		s.Store.detach(s)
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
	v, err := parseVideoURL(r, s.cfg.MaxFileBytes)
	if err == nil && s.cfg.IsResourceHost == nil {
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
	_, err = parseVideoURL(r, maxFileBytes)
	return err
}

func parseVideoURL(r *http.Request, maxFileBytes int64) (video, error) {
	if _, err := videometa.ValidateURL(r.URL, maxFileBytes); err != nil {
		return video{}, err
	}
	return parseVideo(r, maxFileBytes)
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
	clientContext := r.Context()
	w = response
	defer func() {
		crash := recover()
		outcome := response.outcome(clientContext, crash)
		log.Info("request_finished", "status", response.status, "bytes", response.bytes, "elapsed", time.Since(start), "elapsed_ms", time.Since(start).Milliseconds(),
			"response_headers_ms", response.headerLatency.Milliseconds(), "first_body_ms", response.firstBodyMS(),
			"outcome", outcome, "cache", w.Header().Get("X-SWD-Cache"), "content_range", w.Header().Get("Content-Range"),
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
	event := usageEvent{at: start.UnixMilli(), source: "http", method: r.Method, rangeHeader: r.Header.Get("Range")}
	if r.Method == http.MethodGet {
		var finish func()
		response.current, finish = s.beginGET(id, start, r.Header.Get("Range"))
		defer finish()
	}
	defer func() {
		panicked := recover()
		event.status, event.bytes = response.status, response.videoBytes
		event.responseBytes = response.expectedBytes
		if response.videoBytes > 0 {
			group := response.firstBodyBlocking
			event.firstBodyBlocking = &group
		}
		event.firstBodyNS = response.firstBodyLatency.Nanoseconds()
		event.elapsedMS = time.Since(start).Milliseconds()
		event.cache = w.Header().Get("X-SWD-Cache")
		if event.cache == "" {
			event.cache = "UNKNOWN"
		}
		event.outcome = response.outcome(clientContext, panicked)
		s.usage.record(event)
		s.recordTraffic(r.Method, event.cache, event.outcome, event.status, event.bytes)
		if panicked != nil {
			panic(panicked)
		}
	}()
	v, release, err := s.requestVideo(r, observed, response.current)
	response.current.identify(v.songID, v.key)
	event.key, event.host, event.size, event.songID = v.key, v.host, v.size, v.songID
	if release != nil {
		defer release()
	}
	if err != nil {
		if errors.Is(err, ErrSongBlocked) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if errors.Is(err, errPlaybackUpstream) || errors.Is(err, ErrLocalStorage) {
			http.Error(w, "playback resolution failed", cacheErrorStatus(err))
			return
		}
		http.Error(w, err.Error(), 400)
		return
	}
	log = log.With("resource_key", v.key)
	log.Info("request_started")
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
	if r.Method == http.MethodGet {
		ctx, timingErr := s.playbackContext(r, v, start)
		if timingErr != nil {
			http.Error(w, "playback metadata unavailable", cacheErrorStatus(timingErr))
			return
		}
		r = r.WithContext(ctx)
	}
	s.pinVideo(v)
	defer func() { s.releaseVideo(v) }()
	releaseDemand := func() {}
	attachGET := func(resource video) error {
		if r.Method != http.MethodGet {
			return nil
		}
		ctx := context.WithValue(r.Context(), httpGETKey{}, true)
		// A replacement URL receives a fresh source, even for the same content.
		ctx = context.WithValue(ctx, sourceDemandKey{}, (*downloadDemand)(nil))
		demand, release, err := s.addDemand(ctx, resource, true)
		if err != nil {
			return err
		}
		releaseDemand = release
		r = r.WithContext(context.WithValue(ctx, sourceDemandKey{}, demand))
		response.consumption = &getConsumption{s: s, demand: demand, size: resource.size}
		return nil
	}
	if err := attachGET(v); err != nil {
		http.Error(w, "block demand capacity unavailable", cacheErrorStatus(err))
		return
	}
	defer func() { releaseDemand() }()
	if r.Method == http.MethodGet {
		s.usage.startGET(v.songID, v.key)
	}
	// Transfer the request pin and event identity together on every pre-body retry.
	retry := func(cause error) error {
		replacement, fallbackErr := s.retrySongURL(r.Context(), v)
		if fallbackErr != nil {
			return errors.Join(cause, fmt.Errorf("saved URL refresh: %w", fallbackErr))
		}
		if r.Method == http.MethodGet {
			ctx, err := s.playbackContext(r, replacement, start)
			if err != nil {
				return err
			}
			r = r.WithContext(ctx)
		}
		s.pinVideo(replacement)
		releaseDemand()
		if err := attachGET(replacement); err != nil {
			s.releaseVideo(replacement)
			return err
		}
		s.releaseVideo(v)
		v = replacement
		event.key, event.host, event.size = v.key, v.host, v.size
		response.current.identify(v.songID, v.key)
		return nil
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
		f, stream, err = s.obtainDemand(r.Context(), v, false, r.Method == http.MethodGet)
		if err != nil && v.cached != nil && errors.Is(err, errUpstreamDownload) && r.Context().Err() == nil {
			if err = retry(err); err == nil {
				goto loadVideo
			}
		}
	}
	if err != nil {
		if r.Context().Err() != nil {
			log.Info("client_disconnected", "elapsed", time.Since(start))
			return
		}
		log.Error("cache_failed", "error", applog.SafeError(err), "elapsed", time.Since(start))
		status := cacheErrorStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	if stream != nil {
		stream.onWait = func() {
			if response.videoBytes == 0 {
				response.waitedForBlock = true
			}
		}
		pending := serveStream(w, r, v, stream)
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
					if stream.err = retry(stream.err); stream.err == nil {
						goto loadVideo
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
				status := cacheErrorStatus(stream.err)
				http.Error(w, http.StatusText(status), status)
				return
			}
			// Headers may already be sent. Abort rather than completing a truncated body.
			panic(http.ErrAbortHandler)
		}
		pending.commit()
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
	w.Header().Set("X-SWD-Cache", f.source)
	http.ServeContent(w, r, v.path, file.info.ModTime(), file.reader())
	if r.Context().Err() != nil {
		log.Info("client_disconnected", "cache", f.source, "elapsed", time.Since(start))
		return
	}
	log.Info("served", "cache", f.source, "elapsed", time.Since(start))
}

type responseWriter struct {
	http.ResponseWriter
	current                           *currentGET
	started                           time.Time
	headerLatency                     time.Duration
	firstBodyLatency                  time.Duration
	status                            int
	bytes                             int64
	videoBytes                        int64
	expectedBytes                     *int64
	waitedForBlock, firstBodyBlocking bool
	writeErr                          error
	consumption                       *getConsumption
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if w.consumption != nil {
		w.consumption.start(status, w.Header())
		if w.consumption.active && w.Header().Get("Content-Type") == "video/mp4" {
			expected := w.consumption.limit - w.consumption.position
			w.expectedBytes = &expected
		}
	}
	w.headerLatency = time.Since(w.started)
	if w.current != nil {
		w.current.response.Store(&getResponse{status: status, expected: w.expectedBytes})
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.consumption != nil && w.consumption.err != nil {
		w.writeErr = w.consumption.err
		return 0, w.writeErr
	}
	n, err := w.ResponseWriter.Write(p)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}
	if w.consumption != nil && n > 0 {
		w.consumption.accept(int64(n))
	}
	if n > 0 && w.expectedBytes != nil {
		if w.videoBytes == 0 {
			w.firstBodyBlocking = w.waitedForBlock
		}
		w.videoBytes += int64(n)
		if w.current != nil {
			w.current.bytes.Add(int64(n))
		}
	}
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
	status    int
	committed bool
}

func (w *streamResponseWriter) Header() http.Header { return w.header }
func (w *streamResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
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

// Closing here releases failed spools before waiting for a replacement URL.
func serveStream(w http.ResponseWriter, r *http.Request, v video, stream *spoolReader) *streamResponseWriter {
	defer stream.Close()
	pending := &streamResponseWriter{ResponseWriter: w, header: w.Header().Clone()}
	pending.Header().Set("Content-Type", "video/mp4")
	pending.Header().Set("ETag", `"`+v.key+`"`)
	pending.Header().Set("X-SWD-Cache", "MISS")
	http.ServeContent(pending, r, v.path, time.Time{}, stream)
	return pending
}
func cacheErrorStatus(err error) int {
	if errors.Is(err, ErrLocalStorage) || errors.Is(err, errBusy) || errors.Is(err, ErrDemandCapacity) || errors.Is(err, context.Canceled) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}
func (w *responseWriter) outcome(ctx context.Context, crash any) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	if crash != nil {
		return "aborted"
	}
	if w.status >= 400 || w.writeErr != nil || ctx.Err() != nil {
		return "failed"
	}
	if w.expectedBytes != nil && w.videoBytes < *w.expectedBytes {
		return "incomplete"
	}
	return "completed"
}
