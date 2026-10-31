package upstreamcheck

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/playbacksource"
)

const (
	calibrationMaxVideos      = 128
	calibrationPrepareWorkers = 4
)

// Calibration is an opt-in, isolated diagnostic. It does not start the regular
// monitor or publish recommendations. Business callbacks select candidates and
// reuse URL observations; media uses ordinary demands with a discard sink.
type Calibration struct {
	mu      sync.Mutex
	client  *http.Client
	options CalibrationOptions
	state   CalibrationStatus
	saved   *CalibrationResult
	changed chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	closed  bool
	bytes   atomic.Int64
	limits  calibrationLimits
}

type calibrationLimits struct {
	duration, interval time.Duration
	bytes              int64
	warmup, window     time.Duration
	prepare            time.Duration
	reserveVideos      int
	reserveBytes       int64
}

type CalibrationSample struct {
	ElapsedMS   int64    `json:"elapsedMS"`
	WindowMS    int64    `json:"windowMS"`
	Open        int      `json:"open"`
	PeakActive  int      `json:"peakActive"`
	SpeedBPS    float64  `json:"speedBPS"`
	CPUPercent  *float64 `json:"cpuPercent"`
	CPUMSPerMiB *float64 `json:"cpuMSPerMiB"`
}

// Compact completed observations survive retries within this process. No media
// or partial run is retained. The client remains fixed for this instance.
type CalibrationResult struct {
	Finished            time.Time `json:"finished"`
	Run                 uint64    `json:"run"`
	Upstream            string    `json:"upstream"`
	ConcurrencyLimit    int       `json:"concurrencyLimit"`
	FullConcurrency     int       `json:"fullConcurrency"`
	SingleSpeedBPS      *float64  `json:"singleSpeedBPS"`
	FullSpeedBPS        *float64  `json:"fullSpeedBPS"`
	FullCPUPercent      *float64  `json:"fullCPUPercent"`
	CPUP95Percent       *float64  `json:"cpuP95Percent"`
	CPUP99Percent       *float64  `json:"cpuP99Percent"`
	ReferenceBitrateBPS int64     `json:"referenceBitrateBPS"`
}

type CalibrationStatus struct {
	// Finished is the last non-canceled completion, including failures.
	Finished            time.Time           `json:"finished"`
	LastValid           *CalibrationResult  `json:"lastValid"`
	Revision            uint64              `json:"revision"`
	Run                 uint64              `json:"run"`
	Upstream            string              `json:"upstream"`
	Phase               string              `json:"phase"`
	MeasurementStage    string              `json:"measurementStage,omitempty"`
	Reason              string              `json:"reason,omitempty"`
	ResourceHost        string              `json:"resourceHost,omitempty"`
	Running             bool                `json:"running"`
	Closed              bool                `json:"closed"`
	Open                int                 `json:"open"`
	ConcurrencyLimit    int                 `json:"concurrencyLimit"`
	FullConcurrency     int                 `json:"fullConcurrency"`
	PeakOpen            int                 `json:"peakOpen"`
	Active              int                 `json:"active"`
	PeakActive          int                 `json:"peakActive"`
	Bytes               int64               `json:"bytes"`
	Completed           int                 `json:"completed"`
	Failures            int                 `json:"failures"`
	ElapsedMS           int64               `json:"elapsedMS"`
	BlockTimeMS         float64             `json:"blockTimeMS"`
	SingleSpeedBPS      *float64            `json:"singleSpeedBPS"`
	FullSpeedBPS        *float64            `json:"fullSpeedBPS"`
	LiveSingleSpeedBPS  *float64            `json:"liveSingleSpeedBPS"`
	LiveFullSpeedBPS    *float64            `json:"liveFullSpeedBPS"`
	LiveSpeedBPS        *float64            `json:"liveSpeedBPS"`
	FullCPUPercent      *float64            `json:"fullCPUPercent"`
	ReferenceBitrateBPS int64               `json:"referenceBitrateBPS"`
	CPUPercent          *float64            `json:"cpuPercent"`
	CPUP95Percent       *float64            `json:"cpuP95Percent"`
	CPUP99Percent       *float64            `json:"cpuP99Percent"`
	MaxBytes            int64               `json:"maxBytes"`
	MaxDurationMS       int64               `json:"maxDurationMS"`
	PreparedVideos      int                 `json:"preparedVideos"`
	PreparedBytes       int64               `json:"preparedBytes"`
	PreparationProgress int                 `json:"preparationProgress"`
	Samples             []CalibrationSample `json:"samples"`
}

// CalibrationCandidates contains read-only local observations. URLs follow the
// Store's validity and source policy; SongIDs supply local fallbacks for the
// shared business selector.
type CalibrationCandidates struct {
	URLs    []cacheproxy.SongURL
	SongIDs []int64
}

// The application supplies existing business and access policies. Calibration
// only bounds preparation and observes downloads; it never selects channels.
type CalibrationOptions struct {
	Candidates         func(context.Context) CalibrationCandidates
	SelectSong         func(context.Context, int64, map[int64]bool) int64
	Playback           func(context.Context, int64, playbacksource.Entry) (Result, *Sample, PlaybackURLResponse)
	OnPlaybackURL      func(context.Context, PlaybackURLResponse)
	OnFailedURL        func(context.Context, cacheproxy.SongURL)
	ResourceTransports func(string) []http.RoundTripper
}

type calibrationVideo struct {
	videoSample
	observation cacheproxy.SongURL
}

// Candidates supplies fresh observations per run. Saved URLs precede live
// resolution of known songs. Catalog discovery belongs to library synchronization.
func NewCalibration(client *http.Client, upstream string, options CalibrationOptions) *Calibration {
	return &Calibration{client: client, options: options, changed: make(chan struct{}),
		state: CalibrationStatus{Phase: "idle", Upstream: upstream, Samples: []CalibrationSample{}},
		limits: calibrationLimits{duration: 20 * time.Second, interval: time.Second, bytes: 1 << 30,
			warmup: time.Second, window: 3 * time.Second, prepare: 30 * time.Second,
			reserveVideos: 16, reserveBytes: 2 << 30}}
}

// Snapshot and subscription are atomic. A slow consumer can coalesce changes
// and reconnect to a full snapshot without losing a terminal result.
func (c *Calibration) Snapshot() (CalibrationStatus, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	if c.saved != nil {
		saved := *c.saved
		s.LastValid = &saved
	}
	s.ReferenceBitrateBPS = cacheproxy.TypicalBitrate
	s.Samples = append([]CalibrationSample{}, s.Samples...)
	return s, c.changed
}

func (c *Calibration) publishLocked() {
	c.state.Revision++
	close(c.changed)
	c.changed = make(chan struct{})
}

// Start always measures the client supplied by the application owner.
func (c *Calibration) Start() error {
	if c.client == nil {
		return errors.New("当前上游尚未就绪")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("测速已关闭")
	}
	if c.state.Running {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	c.bytes.Store(0)
	c.state = CalibrationStatus{Finished: c.state.Finished, Revision: c.state.Revision, Run: c.state.Run + 1,
		Phase: "preparing", Upstream: c.state.Upstream, Running: true, Samples: []CalibrationSample{},
		MaxBytes: c.limits.bytes, MaxDurationMS: c.limits.duration.Milliseconds()}
	c.publishLocked()
	go c.run(ctx, c.done)
	return nil
}

func (c *Calibration) Cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Running && c.state.Phase != "canceling" {
		c.state.Phase = "canceling"
		c.cancel()
		c.publishLocked()
	}
}

// Shutdown is nonblocking so the HTTP owner can end event streams before drain.
func (c *Calibration) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed, c.state.Closed = true, true
		if c.cancel != nil {
			c.cancel()
		}
		c.publishLocked()
	}
}

func (c *Calibration) Close() {
	c.Shutdown()
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

var errCalibrationPreparationInsufficient = errors.New("preparation_insufficient")

func (c *Calibration) prepare(ctx context.Context) (*http.Client, []calibrationVideo, error) {
	var local CalibrationCandidates
	if c.options.Candidates != nil && ctx.Err() == nil {
		local = c.options.Candidates(ctx)
	}
	var samples []calibrationVideo
	var preparedBytes int64
	ready := func() bool { return len(samples) >= c.limits.reserveVideos && preparedBytes >= c.limits.reserveBytes }
	seenIDs, seenVideos := make(map[int64]bool), make(map[string]bool)
	accept := func(observation cacheproxy.SongURL) bool {
		sample, err := parseSample(observation.URL)
		if err != nil || seenVideos[sample.checksum] {
			return false
		}
		seenVideos[sample.checksum] = true
		samples = append(samples, calibrationVideo{sample, observation})
		preparedBytes += sample.size
		c.mu.Lock()
		c.state.PreparedVideos, c.state.PreparedBytes = len(samples), preparedBytes
		c.state.PreparationProgress = min(100, 100*len(samples)/c.limits.reserveVideos, int(100*preparedBytes/c.limits.reserveBytes))
		c.publishLocked()
		c.mu.Unlock()
		return ready() || len(samples) == calibrationMaxVideos
	}
	for _, saved := range local.URLs {
		if ctx.Err() != nil || ready() || len(samples) == calibrationMaxVideos {
			break
		}
		if saved.SongID <= 0 || seenIDs[saved.SongID] {
			continue
		}
		if _, err := parseSample(saved.URL); err != nil {
			continue
		}
		seenIDs[saved.SongID] = true
		accept(saved)
	}
	if ctx.Err() == nil && !ready() && len(samples) < calibrationMaxVideos && c.options.Playback != nil {
		c.prepareSamples(ctx, local.SongIDs, seenIDs, accept)
	}
	// The reserve is a preparation target, not a prerequisite for measurement.
	// A preparation deadline still leaves received samples available; the run's
	// parent context separately controls user cancellation and shutdown.
	if len(samples) == 0 || errors.Is(ctx.Err(), context.Canceled) {
		return nil, nil, errCalibrationPreparationInsufficient
	}
	return c.client, samples, nil
}

// Resolve metadata only, with bounded parallelism. Cancel and join every probe
// before returning, so preparation cannot contaminate download CPU samples.
func (c *Calibration) prepareSamples(parent context.Context, ids []int64, seen map[int64]bool, accept func(cacheproxy.SongURL) bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var selectMu sync.Mutex
	next := func() int64 {
		selectMu.Lock()
		defer selectMu.Unlock()
		for len(ids) > 0 && (ids[0] <= 0 || seen[ids[0]]) {
			ids = ids[1:]
		}
		if len(ids) == 0 || ctx.Err() != nil {
			return 0
		}
		id := ids[0]
		if c.options.SelectSong != nil {
			id = c.options.SelectSong(ctx, id, seen)
		}
		if id <= 0 || seen[id] || ctx.Err() != nil {
			return 0
		}
		seen[id] = true
		return id
	}
	results := make(chan cacheproxy.SongURL, calibrationPrepareWorkers)
	var wg sync.WaitGroup
	for range min(calibrationPrepareWorkers, len(ids)) {
		wg.Go(func() {
			for {
				id := next()
				if id == 0 {
					return
				}
				_, sample, response := c.options.Playback(ctx, id, playbackRoutes[0])
				if response.URL != "" && c.options.OnPlaybackURL != nil && parent.Err() == nil {
					// Meeting the reserve may cancel sibling probes. Preserve responses
					// already received, but still honor the run's deadline/cancellation.
					c.options.OnPlaybackURL(parent, response)
				}
				if sample == nil {
					continue
				}
				select {
				case results <- response.SongURL():
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() { wg.Wait(); close(results) }()
	for sample := range results {
		if ctx.Err() == nil && accept(sample) {
			cancel()
		}
	}
}

func (c *Calibration) run(ctx context.Context, done chan struct{}) {
	reason := "sample_unavailable"
	defer func() {
		// net/http may keep an unused dial alive after its request is canceled.
		// Drain idle connections without retiring the shared upstream runtime.
		c.client.CloseIdleConnections()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.state.Running, c.state.Active = false, 0
		c.state.Bytes = c.bytes.Load()
		c.state.Phase, c.state.Reason = "completed", reason
		if ctx.Err() != nil {
			c.state.Phase, c.state.Reason = "canceled", "canceled"
			c.state.SingleSpeedBPS, c.state.FullSpeedBPS = nil, nil
			c.state.LiveSingleSpeedBPS, c.state.LiveFullSpeedBPS = nil, nil
			c.state.LiveSpeedBPS = nil
			c.state.FullCPUPercent, c.state.CPUPercent, c.state.CPUP95Percent, c.state.CPUP99Percent = nil, nil, nil, nil
		} else {
			c.state.Finished = time.Now()
			if (reason != "time_limit" && reason != "byte_limit" && reason != "samples_exhausted" && reason != "measurement_complete") || c.state.Bytes == 0 {
				c.state.Phase = "failed"
			}
		}
		if c.state.Phase == "completed" && c.state.SingleSpeedBPS != nil && c.state.FullSpeedBPS != nil {
			c.saved = &CalibrationResult{Finished: c.state.Finished, Run: c.state.Run, Upstream: c.state.Upstream, ConcurrencyLimit: c.state.ConcurrencyLimit, FullConcurrency: c.state.FullConcurrency,
				SingleSpeedBPS: c.state.SingleSpeedBPS, FullSpeedBPS: c.state.FullSpeedBPS, FullCPUPercent: c.state.FullCPUPercent,
				CPUP95Percent: c.state.CPUP95Percent, CPUP99Percent: c.state.CPUP99Percent, ReferenceBitrateBPS: cacheproxy.TypicalBitrate}
		}
		c.cancel()
		c.publishLocked()
		slog.Info("network_calibration_finished", "upstream", c.state.Upstream, "phase", c.state.Phase,
			"reason", c.state.Reason, "prepared_videos", c.state.PreparedVideos,
			"prepared_bytes", c.state.PreparedBytes, "downloaded_bytes", c.state.Bytes)
		close(done)
	}()
	prep, cancel := context.WithTimeout(ctx, c.limits.prepare)
	client, samples, err := c.prepare(prep)
	cancel()
	if err != nil || ctx.Err() != nil {
		if errors.Is(err, errCalibrationPreparationInsufficient) {
			reason = "preparation_insufficient"
		}
		return
	}
	reason = c.download(ctx, client, samples)
}

func (c *Calibration) download(parent context.Context, client *http.Client, samples []calibrationVideo) (reason string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	started := time.Now()
	fullCPU := newCalibrationCPU(started)
	// Overall P99 starts with the actual loader, after all video preparation.
	cpu := newCalibrationCPU(started)
	lastAt, lastBytes := started, int64(0)
	windowPeak := 0
	speeds := newCalibrationSpeeds(c.limits.warmup, c.limits.window)
	liveRate := calibrationLiveRate{}
	liveRate.observe(started, 0)
	var measured atomic.Bool
	updates := make(chan struct{}, 1)
	observeLocked := func(now time.Time, open, active int) {
		fullDuration := speeds.full.duration
		fullOpen := speeds.open
		speeds.observe(now, c.bytes.Load(), open, active)
		used, ok := calibrationCPUTime()
		fullCPU.observeSelected(now, used, ok, speeds.full.duration > fullDuration)
		c.state.FullCPUPercent = fullCPU.average()
		c.state.SingleSpeedBPS, c.state.FullSpeedBPS = speeds.values()
		c.state.LiveSingleSpeedBPS, c.state.LiveFullSpeedBPS = speeds.liveValues()
		c.state.LiveSpeedBPS = liveRate.observe(now, c.bytes.Load())
		if speeds.full.duration > fullDuration {
			c.state.FullConcurrency = fullOpen
		}
		c.state.MeasurementStage = speeds.stage
		if speeds.stage == "complete" {
			measured.Store(true)
			cancel() // Stop outstanding media immediately; the owner drains it.
		}
		select {
		case updates <- struct{}{}:
		default:
		}
	}
	c.mu.Lock()
	if parent.Err() != nil {
		c.mu.Unlock()
		return "canceled"
	}
	c.state.Phase, c.state.Open, c.state.PeakOpen = "running", 1, 1
	c.state.ConcurrencyLimit = 1
	c.state.ResourceHost = samples[0].host
	c.state.MeasurementStage = speeds.stage
	c.publishLocked()
	c.mu.Unlock()
	// Only the ordinary admission ceiling and the output sink differ. Window,
	// ranges, retries and the natural growth cadence remain with the loader.
	var byteLimit atomic.Bool
	cfg := cacheproxy.DiscardConfig{Transport: client.Transport, ResourceTransports: c.options.ResourceTransports}
	cfg.ObserveBytes = func(n int64) {
		if c.bytes.Add(n) >= c.limits.bytes {
			byteLimit.Store(true)
			cancel()
		}
	}
	cfg.Observe = func(e blocksched.Event) {
		c.mu.Lock()
		defer c.mu.Unlock()
		changed := c.state.Open != e.Open || c.state.Active != e.Active || e.Kind == "sample"
		stage := speeds.stage
		observeLocked(time.Now(), e.Open, e.Active)
		changed = changed || stage != speeds.stage
		c.state.Open, c.state.Active = e.Open, e.Active
		c.state.PeakOpen = max(c.state.PeakOpen, e.Open)
		c.state.PeakActive = max(c.state.PeakActive, e.Active)
		if e.Kind == "sample" {
			c.state.BlockTimeMS = float64(e.BlockTime) / float64(time.Millisecond)
			c.state.Completed++
		}
		windowPeak = max(windowPeak, e.Active)
		if changed {
			c.publishLocked()
		}
	}
	engine, err := cacheproxy.NewDiscardLoader(cfg)
	if err != nil {
		return "scheduler_unavailable"
	}
	engine.SetSoftLimit(1)
	captureCPU := func() {
		used, ok := calibrationCPUTime()
		cpu.observe(time.Now(), used, ok)
		avg, p95, p99 := cpu.values()
		c.mu.Lock()
		c.state.CPUPercent, c.state.CPUP95Percent, c.state.CPUP99Percent = avg, p95, p99
		c.publishLocked()
		c.mu.Unlock()
	}
	capture := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		now, received := time.Now(), c.bytes.Load()
		seconds := now.Sub(lastAt).Seconds()
		if seconds <= 0 {
			return
		}
		observeLocked(now, c.state.Open, c.state.Active)
		row := CalibrationSample{ElapsedMS: now.Sub(started).Milliseconds(), WindowMS: now.Sub(lastAt).Milliseconds(),
			Open: c.state.Open, PeakActive: windowPeak, SpeedBPS: float64(received-lastBytes) / seconds}
		c.state.Bytes, c.state.ElapsedMS = received, row.ElapsedMS
		c.state.Samples = append(c.state.Samples, row)
		windowPeak = c.state.Active
		lastAt, lastBytes = now, received
		c.publishLocked()
	}
	ticker := time.NewTicker(c.limits.interval)
	defer ticker.Stop()
	cpuTicker := time.NewTicker(100 * time.Millisecond)
	defer cpuTicker.Stop()
	deadline := time.NewTimer(c.limits.duration)
	defer deadline.Stop()
	type loadResult struct {
		observation cacheproxy.SongURL
		err         error
	}
	finished := make(chan loadResult, len(samples))
	activeLoads := len(samples)
	for _, sample := range samples {
		go func() { finished <- loadResult{sample.observation, engine.Load(ctx, sample.url)} }()
	}
	defer func() {
		captureCPU() // Freeze before draining canceled bodies and other cleanup.
		cancel()
		engine.Close() // Join every worker/body before the terminal event.
		for activeLoads > 0 {
			<-finished
			activeLoads--
		}
		capture()
	}()
	var lastFailure error
	released := false
	for {
		select {
		case <-updates:
			c.mu.Lock()
			stage := speeds.stage
			c.mu.Unlock()
			if stage == "complete" {
				return "measurement_complete"
			}
			if !released && stage == "ramping" {
				released = true
				c.mu.Lock()
				c.state.ConcurrencyLimit = cacheproxy.VideoRequestSoftLimit
				c.publishLocked()
				c.mu.Unlock()
				engine.SetSoftLimit(cacheproxy.VideoRequestSoftLimit)
			}
		case result := <-finished:
			activeLoads--
			if parent.Err() != nil {
				return "canceled"
			}
			if measured.Load() {
				return "measurement_complete"
			}
			if byteLimit.Load() {
				return "byte_limit"
			}
			if result.err != nil {
				lastFailure = result.err
				c.mu.Lock()
				c.state.Failures++
				c.publishLocked()
				c.mu.Unlock()
				// Canceled work and local upstream availability do not invalidate
				// an address. Ordinary failures use the shared URL store.
				if !localObservation(errorState(result.err)) && c.options.OnFailedURL != nil {
					c.options.OnFailedURL(parent, result.observation)
				}
			}
			if activeLoads == 0 {
				if lastFailure != nil {
					return errorState(lastFailure)
				}
				return "samples_exhausted"
			}
		case <-ctx.Done():
			if parent.Err() == nil && measured.Load() {
				return "measurement_complete"
			}
			if parent.Err() == nil && byteLimit.Load() {
				return "byte_limit"
			}
			return "canceled"
		case <-deadline.C:
			if measured.Load() {
				return "measurement_complete"
			}
			return "time_limit"
		case <-ticker.C:
			capture()
		case <-cpuTicker.C:
			captureCPU()
			c.mu.Lock()
			observeLocked(time.Now(), c.state.Open, c.state.Active)
			c.publishLocked()
			c.mu.Unlock()
		}
	}
}
