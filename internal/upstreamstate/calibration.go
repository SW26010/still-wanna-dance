package upstreamstate

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

// Isolated calibration keeps the default engine geometry as its baseline.
const calibrationBlock = cacheproxy.DefaultBlockSize

const (
	calibrationMaxVideos      = 128
	calibrationMaxLoads       = 8
	calibrationPrepareWorkers = 4
	// Leave ample headroom under the scheduler's normal metadata capacity.
	calibrationQueuedBytes = 2 << 30
)

// Calibration is an opt-in, isolated diagnostic. It does not start the regular
// monitor, publish recommendations, or change business storage/settings. Media
// loading uses ordinary resource demands with a streaming discard sink.
type Calibration struct {
	mu      sync.Mutex
	channel *upstreamrequest.Channel
	state   CalibrationStatus
	saved   map[string]CalibrationResult
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
// or partial run is retained, and direct/proxy results cannot replace each other.
type CalibrationResult struct {
	Run                 uint64   `json:"run"`
	Upstream            string   `json:"upstream"`
	ConcurrencyLimit    int      `json:"concurrencyLimit"`
	FullConcurrency     int      `json:"fullConcurrency"`
	SingleSpeedBPS      *float64 `json:"singleSpeedBPS"`
	FullSpeedBPS        *float64 `json:"fullSpeedBPS"`
	FullCPUPercent      *float64 `json:"fullCPUPercent"`
	CPUP95Percent       *float64 `json:"cpuP95Percent"`
	CPUP99Percent       *float64 `json:"cpuP99Percent"`
	ReferenceBitrateBPS int64    `json:"referenceBitrateBPS"`
}

type CalibrationStatus struct {
	LastValid           map[string]CalibrationResult `json:"lastValid"`
	Revision            uint64                       `json:"revision"`
	Run                 uint64                       `json:"run"`
	Upstream            string                       `json:"upstream"`
	Phase               string                       `json:"phase"`
	MeasurementStage    string                       `json:"measurementStage,omitempty"`
	Reason              string                       `json:"reason,omitempty"`
	ResourceHost        string                       `json:"resourceHost,omitempty"`
	Running             bool                         `json:"running"`
	Closed              bool                         `json:"closed"`
	Open                int                          `json:"open"`
	ConcurrencyLimit    int                          `json:"concurrencyLimit"`
	FullConcurrency     int                          `json:"fullConcurrency"`
	PeakOpen            int                          `json:"peakOpen"`
	Active              int                          `json:"active"`
	PeakActive          int                          `json:"peakActive"`
	Bytes               int64                        `json:"bytes"`
	Completed           int                          `json:"completed"`
	Failures            int                          `json:"failures"`
	ElapsedMS           int64                        `json:"elapsedMS"`
	BlockTimeMS         float64                      `json:"blockTimeMS"`
	SingleSpeedBPS      *float64                     `json:"singleSpeedBPS"`
	FullSpeedBPS        *float64                     `json:"fullSpeedBPS"`
	LiveSingleSpeedBPS  *float64                     `json:"liveSingleSpeedBPS"`
	LiveFullSpeedBPS    *float64                     `json:"liveFullSpeedBPS"`
	LiveSpeedBPS        *float64                     `json:"liveSpeedBPS"`
	FullCPUPercent      *float64                     `json:"fullCPUPercent"`
	ReferenceBitrateBPS int64                        `json:"referenceBitrateBPS"`
	CPUPercent          *float64                     `json:"cpuPercent"`
	CPUP95Percent       *float64                     `json:"cpuP95Percent"`
	CPUP99Percent       *float64                     `json:"cpuP99Percent"`
	MaxBytes            int64                        `json:"maxBytes"`
	MaxDurationMS       int64                        `json:"maxDurationMS"`
	PreparedVideos      int                          `json:"preparedVideos"`
	PreparedBytes       int64                        `json:"preparedBytes"`
	PreparationProgress int                          `json:"preparationProgress"`
	Samples             []CalibrationSample          `json:"samples"`
}

func NewCalibration(channel *upstreamrequest.Channel) *Calibration {
	return &Calibration{channel: channel, changed: make(chan struct{}),
		state: CalibrationStatus{Phase: "idle", Upstream: "direct", Samples: []CalibrationSample{}},
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
	s.LastValid = make(map[string]CalibrationResult, len(c.saved))
	for key, result := range c.saved {
		s.LastValid[key] = result
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

func (c *Calibration) Start() error {
	return c.StartWithUpstream(c.channel, "direct", nil)
}

// StartWithUpstream owns release even if the run is rejected or already active.
// A per-run channel lets the console reuse its normal proxy factory without
// replacing the direct diagnostic or the application's active configuration.
func (c *Calibration) StartWithUpstream(channel *upstreamrequest.Channel, upstream string, release func()) error {
	if release == nil {
		release = func() {}
	}
	accepted := false
	defer func() {
		if !accepted {
			release()
		}
	}()
	if channel == nil || (upstream != "direct" && upstream != "proxy") {
		return errors.New("未知测速上游")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("测速已关闭")
	}
	if c.state.Running {
		if c.state.Upstream != upstream {
			return errors.New("请先停止当前测速，再切换上游")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	c.bytes.Store(0)
	c.state = CalibrationStatus{Revision: c.state.Revision, Run: c.state.Run + 1,
		Phase: "preparing", Upstream: upstream, Running: true, Samples: []CalibrationSample{},
		MaxBytes: c.limits.bytes, MaxDurationMS: c.limits.duration.Milliseconds()}
	c.publishLocked()
	accepted = true
	go c.run(ctx, c.done, channel, channel.Snapshot(), upstream, release)
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

func calibrationClient(ctx context.Context, source upstreamrequest.Snapshot, target, upstream string) (*http.Client, error) {
	if source.Candidates == nil {
		return nil, errors.New("channel_unavailable")
	}
	cs, err := source.Candidates.Candidates(ctx, target)
	if err != nil {
		return nil, errors.New("channel_unavailable")
	}
	for _, candidate := range cs {
		if candidate.Mode == upstream || (upstream == "proxy" && candidate.Mode == "socks5") {
			// Pin one candidate for this host; never rotate exits mid-test.
			return requestClient(candidate.Transport), nil
		}
	}
	return nil, errors.New("channel_unavailable")
}

var errCalibrationPreparationInsufficient = errors.New("preparation_insufficient")

// Filled during preparation, then immutable while all videos share one loader
// and scheduler. Candidate transports are bound to their resource host.
type calibrationTransports map[string]http.RoundTripper

func (t calibrationTransports) RoundTrip(r *http.Request) (*http.Response, error) {
	transport := t[strings.ToLower(r.URL.Host)]
	if transport == nil {
		return nil, upstreamrequest.ErrUnavailable
	}
	return transport.RoundTrip(r)
}

func (t calibrationTransports) CloseIdleConnections() {
	for _, transport := range t {
		if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

func (c *Calibration) prepare(ctx context.Context, channel *upstreamrequest.Channel, source upstreamrequest.Snapshot, upstream string) (*http.Client, []videoSample, error) {
	p := DefaultPolicy()
	p.RequestTimeout = 10 * time.Second
	var samples []videoSample
	var preparedBytes int64
	selected := make(calibrationTransports)
	ready := func() bool { return len(samples) >= c.limits.reserveVideos && preparedBytes >= c.limits.reserveBytes }
	seenIDs, seenVideos := make(map[int64]bool), make(map[string]bool)
	for _, routeID := range routeIDs(Catalog) {
		if ctx.Err() != nil || ready() || len(samples) == calibrationMaxVideos {
			break
		}
		catalogClient, err := calibrationClient(ctx, source, entry(Catalog, routeID), upstream)
		if err != nil {
			continue
		}
		o, id, catalog := probeCatalogData(ctx, catalogClient, p, routeID)
		if o.state != "available" || id == 0 {
			continue
		}
		client, err := calibrationClient(ctx, source, entry(PlaybackURL, playbackRoutes[0].ID), upstream)
		if err != nil {
			continue
		}
		ids := calibrationSongIDs(catalog)
		if len(ids) == 0 {
			ids = []int64{id}
		}
		rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		pending := make([]int64, 0, 2*calibrationMaxVideos)
		for _, id := range ids {
			if seenIDs[id] {
				continue
			}
			seenIDs[id] = true
			pending = append(pending, id)
			if len(pending) == cap(pending) {
				break
			}
		}
		prepareCalibrationSamples(ctx, client, p, pending, func(sample videoSample) bool {
			if sample.size < 16*calibrationBlock || seenVideos[sample.checksum] {
				return false
			}
			host := strings.ToLower(sample.host)
			if selected[host] == nil {
				client, err := calibrationClient(ctx, source, sample.url, upstream)
				if err != nil {
					return false
				}
				selected[host] = client.Transport
			}
			seenVideos[sample.checksum] = true
			samples = append(samples, sample)
			preparedBytes += sample.size
			c.mu.Lock()
			c.state.PreparedVideos, c.state.PreparedBytes = len(samples), preparedBytes
			c.state.PreparationProgress = min(100, 100*len(samples)/c.limits.reserveVideos, int(100*preparedBytes/c.limits.reserveBytes))
			c.publishLocked()
			c.mu.Unlock()
			return ready() || len(samples) == calibrationMaxVideos
		})
	}
	if !ready() || ctx.Err() != nil {
		selected.CloseIdleConnections()
		return nil, nil, errCalibrationPreparationInsufficient
	}
	hosts := make([]string, 0, len(samples))
	for _, sample := range samples {
		hosts = append(hosts, sample.host)
	}
	channel.ObserveResourceDomainsAtRevision(source.Revision, "calibration", hosts, time.Minute)
	return requestClient(selected), samples, nil
}

// Resolve metadata only, with bounded parallelism. Cancel and join every probe
// before returning, so preparation cannot contaminate download CPU samples.
func prepareCalibrationSamples(parent context.Context, client *http.Client, p Policy, ids []int64, accept func(videoSample) bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := make(chan int64, len(ids))
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	results := make(chan videoSample, calibrationPrepareWorkers)
	var wg sync.WaitGroup
	for range min(calibrationPrepareWorkers, len(ids)) {
		wg.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				_, sample := probePlayback(ctx, client, p, id, playbackRoutes[0])
				if sample == nil {
					continue
				}
				select {
				case results <- *sample:
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

// This response has already passed its route's catalog validation. Video URLs
// and metadata still go through the normal resolver before any download.
func calibrationSongIDs(c CatalogResponse) []int64 {
	type song struct {
		ID int64 `json:"id"`
	}
	var data struct {
		Groups struct {
			Contents []struct {
				Songs []song `json:"songInfos"`
			} `json:"contents"`
		} `json:"groups"`
		Data struct {
			Groups []struct {
				Songs []song `json:"entries"`
			} `json:"groups"`
		} `json:"data"`
	}
	if json.Unmarshal(c.Body, &data) != nil {
		return nil
	}
	var ids []int64
	seen := make(map[int64]bool)
	add := func(songs []song) {
		for _, s := range songs {
			if s.ID > 0 && !seen[s.ID] {
				seen[s.ID] = true
				ids = append(ids, s.ID)
			}
		}
	}
	for _, group := range data.Groups.Contents {
		add(group.Songs)
	}
	for _, group := range data.Data.Groups {
		add(group.Songs)
	}
	return ids
}

func (c *Calibration) run(ctx context.Context, done chan struct{}, channel *upstreamrequest.Channel, source upstreamrequest.Snapshot, upstream string, release func()) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	reason := "sample_unavailable"
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-source.Changed:
			stop()
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		channel.ObserveResourceDomainsAtRevision(source.Revision, "calibration", nil, 0)
		release()
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
		} else if (reason != "time_limit" && reason != "byte_limit" && reason != "samples_exhausted" && reason != "measurement_complete") || c.state.Bytes == 0 {
			c.state.Phase = "failed"
		}
		if c.state.Phase == "completed" && c.state.SingleSpeedBPS != nil && c.state.FullSpeedBPS != nil {
			if c.saved == nil {
				c.saved = make(map[string]CalibrationResult)
			}
			c.saved[upstream] = CalibrationResult{Run: c.state.Run, Upstream: upstream, ConcurrencyLimit: c.state.ConcurrencyLimit, FullConcurrency: c.state.FullConcurrency,
				SingleSpeedBPS: c.state.SingleSpeedBPS, FullSpeedBPS: c.state.FullSpeedBPS, FullCPUPercent: c.state.FullCPUPercent,
				CPUP95Percent: c.state.CPUP95Percent, CPUP99Percent: c.state.CPUP99Percent, ReferenceBitrateBPS: cacheproxy.TypicalBitrate}
		}
		c.cancel()
		c.publishLocked()
		close(done)
	}()
	prep, cancel := context.WithTimeout(ctx, c.limits.prepare)
	client, samples, err := c.prepare(prep, channel, source, upstream)
	cancel()
	if client != nil {
		defer client.CloseIdleConnections()
	}
	if err != nil || ctx.Err() != nil {
		if errors.Is(err, errCalibrationPreparationInsufficient) {
			reason = "preparation_insufficient"
		}
		return
	}
	reason = c.download(ctx, client, samples)
}

func (c *Calibration) download(parent context.Context, client *http.Client, samples []videoSample) (reason string) {
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
	cfg := cacheproxy.DiscardConfig{Transport: client.Transport}
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
		bytes int64
		err   error
	}
	finished := make(chan loadResult, calibrationMaxLoads)
	next, activeLoads := 0, 0
	var queuedBytes int64
	fill := func() {
		for activeLoads < calibrationMaxLoads && next < len(samples) && ctx.Err() == nil {
			bytes := (samples[next].size + calibrationBlock - 1) / calibrationBlock * calibrationBlock
			if queuedBytes+bytes > calibrationQueuedBytes {
				break
			}
			target := samples[next].url
			next++
			activeLoads++
			queuedBytes += bytes
			go func() { finished <- loadResult{bytes, engine.Load(ctx, target)} }()
		}
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
	fill()
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
			queuedBytes -= result.bytes
			if parent.Err() != nil {
				return "canceled"
			}
			if measured.Load() {
				return "measurement_complete"
			}
			if byteLimit.Load() {
				return "byte_limit"
			}
			if result.err == nil {
				fill()
				if activeLoads == 0 {
					c.mu.Lock()
					complete := speeds.stage == "complete"
					c.mu.Unlock()
					if complete {
						return "measurement_complete"
					}
					return "samples_exhausted"
				}
				continue
			}
			c.mu.Lock()
			c.state.Failures++
			c.mu.Unlock()
			return errorState(result.err)
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
