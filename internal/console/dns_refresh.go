package console

import (
	"context"
	"errors"
	"slices"
	"time"
)

// DNSPolicy controls the existing resolver's refresh behavior, without changing
// the lookup/dial contract. IdleTimeout bounds work after a domain stops being used.
type DNSPolicy struct {
	RefreshAhead time.Duration
	MinInterval  time.Duration
	IdleTimeout  time.Duration
	// QueryTimeout caps a shared lookup. The underlying DoH resolver also has
	// a fixed six-second timeout; larger values do not extend that deadline.
	QueryTimeout time.Duration
}

func DefaultDNSPolicy() DNSPolicy {
	return DNSPolicy{RefreshAhead: time.Minute, MinInterval: 30 * time.Second, IdleTimeout: 10 * time.Minute, QueryTimeout: 6 * time.Second}
}

type dnsState struct {
	query                               dnsQuery
	lastUsed, attempted, observed, next time.Time
	latency                             time.Duration
	err                                 error
	timer                               *time.Timer
}

// SetDNSPolicy changes the running resolver's policy; callers need no changes
// to benefit from automatic refresh with the default policy.
func (c *Console) SetDNSPolicy(p DNSPolicy) error { return c.dns.setPolicy(p) }

func (d *directDNS) initLocked() {
	if d.ctx != nil {
		return
	}
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.states = make(map[string]*dnsState)
	if d.policy == (DNSPolicy{}) {
		d.policy = DefaultDNSPolicy()
	}
	if d.cache == nil {
		d.cache = make(map[string]dnsEntry)
	}
	if d.lookups == nil {
		d.lookups = make(map[string]*dnsLookup)
	}
}

func (d *directDNS) setPolicy(p DNSPolicy) error {
	if p.RefreshAhead < 0 || p.MinInterval <= 0 || p.IdleTimeout <= 0 || p.QueryTimeout <= 0 {
		return errors.New("invalid DNS refresh policy")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("DNS resolver closed")
	}
	d.initLocked()
	d.policy = p
	for host, s := range d.states {
		d.nextLocked(host, s)
		d.scheduleLocked(host, s)
	}
	return nil
}

// queryLocked coalesces background refresh and foreground cache misses.
func (d *directDNS) queryLocked(parent context.Context, host string, s *dnsState) *dnsLookup {
	if call := d.lookups[host]; call != nil {
		return call
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	call := &dnsLookup{done: make(chan struct{})}
	d.lookups[host] = call
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), d.policy.QueryTimeout)
	stop := context.AfterFunc(d.ctx, cancel)
	s.attempted = time.Now()
	query := s.query
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer cancel()
		defer stop()
		ips, ttl, err := query(ctx)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil && len(ips) == 0 {
			err = errors.New("DNS returned no addresses")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		s.latency, s.err = time.Since(s.attempted), err
		if err == nil {
			ips = slices.Clone(ips)
			// Keep a proven address first only while it remains in the new answer.
			if old := d.cache[host].ips; len(old) > 0 {
				if i := slices.Index(ips, old[0]); i > 0 {
					copy(ips[1:i+1], ips[:i])
					ips[0] = old[0]
				}
			}
			s.observed = time.Now()
			d.cache[host] = dnsEntry{ips, s.observed.Add(max(ttl, 0))}
			call.ips = slices.Clone(ips)
		}
		call.err = err
		delete(d.lookups, host)
		d.nextLocked(host, s)
		d.scheduleLocked(host, s)
		close(call.done)
	}()
	return call
}

func (d *directDNS) nextLocked(host string, s *dnsState) {
	s.next = d.cache[host].until.Add(-d.policy.RefreshAhead)
	if earliest := s.attempted.Add(d.policy.MinInterval); s.next.Before(earliest) {
		s.next = earliest
	}
}

func (d *directDNS) scheduleLocked(host string, s *dnsState) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if d.closed || d.paused || d.lookups[host] != nil {
		return
	}
	if s.next.IsZero() {
		d.nextLocked(host, s)
	}
	at := s.next
	if idle := s.lastUsed.Add(d.policy.IdleTimeout); idle.Before(at) {
		at = idle
	}
	// Capture timer identity: Stop can race a callback already waiting for mu.
	var timer *time.Timer
	timer = time.AfterFunc(max(time.Until(at), 0), func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.closed || d.paused || d.states[host] != s || s.timer != timer {
			return
		}
		s.timer = nil
		if time.Since(s.lastUsed) >= d.policy.IdleTimeout {
			delete(d.states, host)
			delete(d.cache, host)
			return
		}
		d.queryLocked(d.ctx, host, s)
	})
	s.timer = timer
}

// Switching to a proxy stops direct background traffic. Existing foreground
// requests may still finish using their transport snapshot.
func (d *directDNS) setBackgroundEnabled(enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paused = !enabled
	for host, s := range d.states {
		d.scheduleLocked(host, s)
	}
}

func (d *directDNS) close() {
	d.mu.Lock()
	d.closed = true
	if d.cancel != nil {
		d.cancel()
	}
	for _, s := range d.states {
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
	}
	d.mu.Unlock()
	d.wg.Wait()
}
