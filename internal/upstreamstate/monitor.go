package upstreamstate

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type Options struct {
	// Channel selects an application-owned observation scope. Nil uses Default.
	// Diagnostics can supply a private channel without publishing probe transports
	// or recommendations to business consumers. The owner closes its transports.
	Channel *upstreamrequest.Channel
	// OnCatalog receives successful responses once per batch, outside monitor locks.
	// It must respect cancellation; Close waits for it to return.
	OnCatalog func(context.Context, []CatalogResponse)
	// OnPlaybackURL receives each valid API response outside monitor locks,
	// before resource probing. Calls may overlap across nodes/channels; the
	// consumer must respect cancellation. Close waits for callbacks to return.
	OnPlaybackURL func(context.Context, PlaybackURLResponse)
	// OnProgress receives phase boundaries and completed results outside locks.
	// Calls within a batch are sequential; Close cancels and waits for callbacks.
	OnProgress func(context.Context, CheckProgress)
	// PlaybackSongCandidates returns incomplete songs in business priority order.
	// Called once per playback batch after catalog delivery, outside monitor locks.
	// Nil/empty retains the normal catalog sample. Close cancels/waits for it.
	PlaybackSongCandidates func(context.Context) []int64
	Policy                 Policy
	ThroughputStatePath    string
}
type Status struct {
	LastThroughputAttempt time.Time `json:"lastThroughputAttempt"`
	NextThroughput        time.Time `json:"nextThroughput"`
	ThroughputStatus      string    `json:"throughputStatus"`
	ThroughputSaveFailed  bool      `json:"throughputSaveFailed"`
	Manual                bool      `json:"manual"`
	CheckKind             CheckKind `json:"checkKind,omitempty"`
	ResourcesPaused       bool      `json:"resourcesPaused"`
	Checking              bool      `json:"checking"`
	Scheduled             bool      `json:"scheduled"`
	Closed                bool      `json:"closed"`
	Started               time.Time `json:"started"`
	Finished              time.Time `json:"finished"`
	NextCheck             time.Time `json:"nextCheck"`
	Results               []Result  `json:"results"`
	Policy                Policy    `json:"policy"`
}

// Monitor is independent of consumers and network configuration. Nothing starts
// automatically; the owner explicitly starts and closes the monitor.
type Monitor struct {
	diagnostics            map[string]observation
	resourceSources        map[string]observation
	onCatalog              func(context.Context, []CatalogResponse)
	onPlaybackURL          func(context.Context, PlaybackURLResponse)
	onProgress             func(context.Context, CheckProgress)
	playbackSongCandidates func(context.Context) []int64
	playbackSongsAttempted map[int64]bool
	throughputSaveFailed   bool
	batchKind              CheckKind
	batchCtx               context.Context
	batchCancel            context.CancelFunc
	manualDone             chan struct{}
	throughputStatePath    string
	lastThroughput         time.Time
	batchThroughput        *bool
	throughput             map[string]observation
	batchManual            bool
	mu                     sync.Mutex
	ctx                    context.Context
	cancel                 context.CancelFunc
	policy                 Policy
	started                time.Time
	finished               time.Time
	nextCheck              time.Time
	history                map[string][]observation
	songID                 int64
	songAt                 time.Time
	active                 chan struct{}
	scheduler              chan struct{}
	closed                 bool
	channel                *upstreamrequest.Channel
	revision               uint64
	batchRevision          uint64
	preferences            map[Operation]*preference
}

func NewMonitor(o Options) (*Monitor, error) {
	channel := o.Channel
	if channel == nil {
		channel = upstreamrequest.Default
	}
	return newMonitor(o, channel)
}

// Both business and scoped diagnostics use the same measurement implementation.
func newMonitor(o Options, channel *upstreamrequest.Channel) (*Monitor, error) {
	p := normalized(o.Policy)
	if err := validatePolicy(p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	snapshot := channel.Snapshot()
	m := &Monitor{ctx: ctx, cancel: cancel, channel: channel, revision: snapshot.Revision, policy: p, history: make(map[string][]observation), throughputStatePath: o.ThroughputStatePath, throughput: make(map[string]observation)}
	m.loadThroughputTime()
	m.onCatalog = o.OnCatalog
	m.onPlaybackURL = o.OnPlaybackURL
	m.onProgress = o.OnProgress
	m.playbackSongCandidates = o.PlaybackSongCandidates
	return m, nil
}

func requestClient(t http.RoundTripper) *http.Client {
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// mu is held. Changes discard affected paths before any reader can reuse them.
func (m *Monitor) refreshChannelLocked() upstreamrequest.Snapshot {
	s := m.channel.Snapshot()
	if s.Revision != m.revision {
		m.playbackSongsAttempted = nil
		m.diagnostics = nil
		m.revision = s.Revision
		if s.Candidates != nil {
			// Retain unchanged direct paths. Proxy configurations have distinct
			// opaque identities, so replaced credentials cannot inherit samples.
			valid := make(map[string]bool)
			for _, op := range operations {
				for _, route := range m.routeIDsLocked(op) {
					for _, c := range s.Candidates.Current(entry(op, route)) {
						valid[string(op)+"/"+route+"/"+c.ID] = true
					}
				}
			}
			for key := range m.history {
				if !valid[key] {
					delete(m.history, key)
				}
			}
			for key := range m.resourceSources {
				if !valid[key] {
					m.removeResourceSourceLocked(key)
				}
			}
		} else {
			m.resourceSources = nil
			m.history = make(map[string][]observation)
			m.throughput = make(map[string]observation)
			m.preferences = nil
			m.songID = 0
			m.songAt = time.Time{}
		}
		m.finished = time.Time{}
		m.nextCheck = time.Time{}
	}
	return s
}
func validatePolicy(p Policy) error {
	if p.ThroughputInterval <= 0 {
		return errors.New("throughput interval must be positive")
	}
	if p.ResourceMaxBytes <= 0 || p.ResourceMaxBytes > 16<<20 || p.ResourceTimeout > 3*time.Second {
		return errors.New("resource measurement requires bytes <= 16 MiB and timeout <= 3 seconds")
	}
	if math.IsNaN(p.SwitchImprovement) || p.SwitchImprovement <= 0 || p.SwitchImprovement >= 1 || p.SwitchSamples < 1 {
		return errors.New("invalid switching policy")
	}
	if p.Interval <= 0 || p.Lifetime <= 0 || p.FailureLifetime <= 0 || p.SampleLifetime <= 0 || p.RequestTimeout <= 0 || p.ResourceTimeout <= 0 {
		return errors.New("policy durations must be positive")
	}
	return nil
}

// Results returns every known route for the operation in definition order,
// including unknown, failed, stale, and closed results. Unsupported operations
// return an empty slice. Results and Snapshot never start checks or rank routes.
func (m *Monitor) Results(op Operation) []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshChannelLocked()
	return m.resultsLocked(op, time.Now())
}
func (m *Monitor) Snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshChannelLocked()
	s := Status{
		Manual:          m.manualDone != nil || (m.active != nil && m.batchManual),
		CheckKind:       m.batchKind,
		ResourcesPaused: m.channel.ResourceBusy() && (m.active == nil || !m.batchManual),
		Checking:        m.active != nil,
		Scheduled:       m.scheduler != nil && !m.closed,
		Closed:          m.closed,
		Started:         m.started,
		Finished:        m.finished,
		NextCheck:       m.nextCheck,
		Policy:          m.policy,
		Results:         make([]Result, 0, len(operations)),
	}
	now := time.Now()
	for _, op := range operations {
		s.Results = append(s.Results, m.resultsLocked(op, now)...)
	}
	s.LastThroughputAttempt = m.lastThroughput
	if !m.lastThroughput.IsZero() {
		s.NextThroughput = m.lastThroughput.Add(m.policy.ThroughputInterval)
	}
	s.ThroughputSaveFailed = m.throughputSaveFailed
	s.ThroughputStatus = m.throughputStatus(s, now)
	return s
}
func (m *Monitor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("monitor closed")
	}
	if m.scheduler != nil {
		return nil
	}
	m.scheduler = make(chan struct{})
	go func() {
		defer close(m.scheduler)
		var candidatesChanged <-chan struct{}
		var subscribedRevision uint64
		for {
			m.mu.Lock()
			source := m.refreshChannelLocked()
			delay := time.Until(m.nextCheck)
			m.mu.Unlock()
			// Retain the subscription until consumed, including across Check and
			// timer wakeups. Re-reading Changed earlier can lose an event.
			if subscribedRevision != source.Revision {
				candidatesChanged = nil
				if source.Candidates != nil {
					candidatesChanged = source.Candidates.Changed()
				}
				subscribedRevision = source.Revision
			}
			if source.Transport == nil {
				select {
				case <-m.ctx.Done():
					return
				case <-source.Changed:
					continue
				}
			}
			if delay <= 0 {
				if err := m.checkRequest(m.ctx, false); err != nil && !errors.Is(err, upstreamrequest.ErrUnavailable) {
					return
				}
				select {
				case <-candidatesChanged:
					candidatesChanged = source.Candidates.Changed()
					m.mu.Lock()
					if !m.closed {
						m.nextCheck = time.Time{}
					}
					m.mu.Unlock()
				default:
				}
				continue
			}
			timer := time.NewTimer(delay)
			select {
			case <-m.ctx.Done():
				timer.Stop()
				return
			case <-source.Changed:
				timer.Stop()
			case <-candidatesChanged:
				timer.Stop()
				candidatesChanged = source.Candidates.Changed()
				m.mu.Lock()
				m.nextCheck = time.Time{}
				m.mu.Unlock()
			case <-timer.C:
			}
		}
	}()
	return nil
}

// Concurrent callers share a check. Caller cancellation stops only that wait;
// Close cancels shared work. Every request has a policy-controlled deadline.
func (m *Monitor) Check(ctx context.Context) error {
	return m.checkRequest(ctx, true)
}

// Only automatically started batches yield bandwidth to business traffic.
// Existing single-flight behavior remains unchanged for explicit Check calls.
func (m *Monitor) checkRequest(ctx context.Context, manual bool, selected ...CheckKind) error {
	var kind CheckKind
	if len(selected) > 0 {
		kind = selected[0]
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return errors.New("monitor closed")
		}
		if m.manualDone != nil && (!manual || kind == "") {
			done := m.manualDone
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			case <-m.ctx.Done():
				return m.ctx.Err()
			}
		}
		joined := m.active != nil
		if m.active == nil {
			source := m.refreshChannelLocked()
			if source.Transport == nil {
				m.mu.Unlock()
				return upstreamrequest.ErrUnavailable
			}
			m.batchRevision = source.Revision
			m.batchManual = manual
			m.batchKind = kind
			m.batchCtx, m.batchCancel = context.WithCancel(m.ctx)
			m.batchThroughput = nil
			m.active = make(chan struct{})
			m.started = time.Now()
			stopCaller := func() bool { return false }
			if kind != "" {
				// A selected check owns its reserved batch; canceling it must
				// not require closing the monitor and losing traversal state.
				stopCaller = context.AfterFunc(ctx, m.batchCancel)
			}
			go func(done chan struct{}) {
				defer stopCaller()
				m.check(done, source)
			}(m.active)
		}
		done := m.active
		revision := m.batchRevision
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-done:
			if joined && kind != "" {
				continue
			}
			if m.channel.Snapshot().Revision != revision {
				continue
			}
			return nil
		}
	}
}
func (m *Monitor) Close() {
	m.mu.Lock()
	m.closed = true
	for key := range m.resourceSources {
		m.removeResourceSourceLocked(key)
	}
	m.nextCheck = time.Time{}
	m.cancel()
	active, scheduler := m.active, m.scheduler
	m.mu.Unlock()
	if active != nil {
		<-active
	}
	if scheduler != nil {
		<-scheduler
	}
}
func (m *Monitor) check(done chan struct{}, source upstreamrequest.Snapshot) {
	// Check has verified the channel. Its snapshot belongs to this batch;
	// channel changes cancel the batch instead of replacing its dependencies.
	ctx, cancel := m.batchCtx, m.batchCancel
	kind := m.batchKind
	defer cancel()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-source.Changed:
			cancel()
		case <-watchDone:
		}
	}()
	record := func(o observation) {
		if m.refreshChannelLocked().Revision == source.Revision {
			if kind.includes(o.op) {
				m.record(o)
			} else {
				m.observePlaybackDomains(o)
			}
		}
	}
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.active = nil
		if !m.closed && m.refreshChannelLocked().Revision == source.Revision {
			m.finished = time.Now()
			if m.scheduler != nil {
				m.nextCheck = m.finished.Add(m.policy.Interval)
				m.scheduleThroughputLocked()
				if source.Candidates != nil {
					m.scheduleCandidatesLocked(source.Candidates)
				}
			}
		}
		close(done)
	}()
	p := m.policy
	defer m.publishProgress(ctx, "")
	m.publishProgress(ctx, Catalog)
	m.checkRoutes(ctx, p, source, record)
}
