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
	Policy Policy
}
type Status struct {
	Checking  bool      `json:"checking"`
	Scheduled bool      `json:"scheduled"`
	Closed    bool      `json:"closed"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	NextCheck time.Time `json:"nextCheck"`
	Results   []Result  `json:"results"`
	Policy    Policy    `json:"policy"`
}

// Monitor is independent of consumers and network configuration. Nothing starts
// automatically; the owner explicitly starts and closes the monitor.
type Monitor struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	policy        Policy
	started       time.Time
	finished      time.Time
	nextCheck     time.Time
	history       map[string][]observation
	songID        int64
	songAt        time.Time
	active        chan struct{}
	scheduler     chan struct{}
	closed        bool
	wake          chan struct{}
	channel       *upstreamrequest.Channel
	revision      uint64
	batchRevision uint64
	preferences   map[Operation]*preference
}

func NewMonitor(o Options) (*Monitor, error) {
	return newMonitor(o, upstreamrequest.Default)
}

// The request-channel seam is private. Callers only configure check policy.
func newMonitor(o Options, channel *upstreamrequest.Channel) (*Monitor, error) {
	p := normalized(o.Policy)
	if err := validatePolicy(p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	snapshot := channel.Snapshot()
	return &Monitor{ctx: ctx, cancel: cancel, channel: channel, revision: snapshot.Revision, policy: p, history: make(map[string][]observation), wake: make(chan struct{}, 1)}, nil
}

func requestClient(t http.RoundTripper) *http.Client {
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// mu is held. Changes discard affected paths before any reader can reuse them.
func (m *Monitor) refreshChannelLocked() upstreamrequest.Snapshot {
	s := m.channel.Snapshot()
	if s.Revision != m.revision {
		m.revision = s.Revision
		if s.Candidates != nil {
			// Retain unchanged direct paths. Proxy configurations have distinct
			// opaque identities, so replaced credentials cannot inherit samples.
			valid := make(map[string]bool)
			for _, op := range operations {
				for _, route := range routeIDs(op) {
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
		} else {
			m.history = make(map[string][]observation)
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
	if p.ResourceMinBytes <= 0 || p.ResourceMinDuration <= 0 || p.ResourceMinDuration > p.ResourceTimeout || p.ResourceTimeout > 5*time.Second {
		return errors.New("resource measurement requires 0 < minimum <= timeout <= 5 seconds")
	}
	if math.IsNaN(p.SwitchImprovement) || p.SwitchImprovement <= 0 || p.SwitchImprovement >= 1 || p.SwitchSamples < 1 {
		return errors.New("invalid switching policy")
	}
	if p.Interval <= 0 || p.Lifetime <= 0 || p.FailureLifetime <= 0 || p.SampleLifetime <= 0 || p.RequestTimeout <= 0 || p.ResourceTimeout <= 0 {
		return errors.New("policy durations must be positive")
	}
	return nil
}

// Changes freshness relative to original observation times, never renewing old
// data. Updated request deadlines apply to the next batch.
func (m *Monitor) SetPolicy(p Policy) error {
	p = normalized(p)
	if err := validatePolicy(p); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("monitor closed")
	}
	m.policy = p
	if m.scheduler != nil && !m.finished.IsZero() {
		m.nextCheck = m.finished.Add(p.Interval)
		if source := m.refreshChannelLocked(); source.Candidates != nil {
			m.scheduleCandidatesLocked(source.Candidates)
		}
	}
	select {
	case m.wake <- struct{}{}:
	default:
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
		Checking:  m.active != nil,
		Scheduled: m.scheduler != nil && !m.closed,
		Closed:    m.closed,
		Started:   m.started,
		Finished:  m.finished,
		NextCheck: m.nextCheck,
		Policy:    m.policy,
		Results:   make([]Result, 0, len(operations)),
	}
	now := time.Now()
	for _, op := range operations {
		s.Results = append(s.Results, m.resultsLocked(op, now)...)
	}
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
			// timer/policy wakeups. Re-reading Changed earlier can lose an event.
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
				if err := m.Check(m.ctx); err != nil && !errors.Is(err, upstreamrequest.ErrUnavailable) {
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
			case <-m.wake:
				timer.Stop()
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
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return errors.New("monitor closed")
		}
		if m.active == nil {
			source := m.refreshChannelLocked()
			if source.Transport == nil {
				m.mu.Unlock()
				return upstreamrequest.ErrUnavailable
			}
			m.batchRevision = source.Revision
			m.active = make(chan struct{})
			m.started = time.Now()
			go m.check(m.active, source)
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
	// Check has verified the channel. This client belongs only to this batch;
	// channel changes cancel the batch instead of replacing its dependencies.
	client := requestClient(source.Transport)
	ctx, cancel := context.WithCancel(m.ctx)
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
			m.record(o)
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
				if source.Candidates != nil {
					m.scheduleCandidatesLocked(source.Candidates)
				}
			}
		}
		close(done)
	}()
	m.mu.Lock()
	p := m.policy
	m.mu.Unlock()
	if source.Candidates != nil {
		m.checkCandidates(ctx, p, source, record)
		return
	}
	catalog, id := probeCatalog(ctx, client, p)
	m.mu.Lock()
	record(catalog)
	if id > 0 && !m.closed && m.revision == source.Revision {
		m.songID = id
		m.songAt = catalog.at
	}
	if !time.Now().Before(m.songAt.Add(m.policy.SampleLifetime)) {
		m.songID = 0
	}
	id = m.songID
	m.mu.Unlock()
	if id == 0 || ctx.Err() != nil {
		return
	}
	var wg sync.WaitGroup
	// Ordinary transports use the same single-transfer rule as candidates.
	resourceSlot := make(chan struct{}, 1)
	for _, r := range videoRoutes {
		wg.Add(1)
		go func(r route) {
			defer wg.Done()
			resolved, sample := probePlayback(ctx, client, p, id, r)
			m.mu.Lock()
			record(resolved)
			m.mu.Unlock()
			if sample == nil {
				if resolved.state != "canceled" {
					o := resolved
					o.op = Resource
					o.state = "resolution_unavailable"
					m.mu.Lock()
					record(o)
					m.mu.Unlock()
				}
				return
			}
			select {
			case resourceSlot <- struct{}{}:
			case <-ctx.Done():
				return
			}
			resource := probeResource(ctx, client, p, id, r, *sample)
			<-resourceSlot
			m.mu.Lock()
			record(resource)
			m.mu.Unlock()
		}(r)
	}
	wg.Wait()
}
