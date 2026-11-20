package cacheproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/upstreamrequest"
)

type networkAttempt struct {
	history              *DownloadRoutes
	parent               context.Context
	key                  routeKey
	peer                 string // ordinary direct connection; guarded by history.mu
	peerReleased         bool   // ignore late trace callbacks after release
	reserved             bool
	started              time.Time
	source               string
	sharedSampleDeadline bool // Earlier DNS discovery or redirects used part of the sample window.
	once                 sync.Once
}

func (a *networkAttempt) trace(ctx context.Context, changed func()) context.Context {
	setPeer := func(ip string) {
		h := a.history
		h.mu.Lock()
		if a.peerReleased || a.peer == ip {
			h.mu.Unlock()
			return
		}
		if a.peer != "" {
			h.entries[routeKey{a.key.host, a.peer}].inFlight--
		}
		if ip != "" {
			h.entry(routeKey{a.key.host, ip}, time.Now()).inFlight++
		}
		a.peer = ip
		h.mu.Unlock()
		changed()
	}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { setPeer("") },
		GotConn: func(info httptrace.GotConnInfo) {
			if addr, ok := info.Conn.RemoteAddr().(*net.TCPAddr); ok && addr.IP.To16() != nil {
				setPeer(addr.IP.String())
			}
		}})
}

func (a *networkAttempt) currentIP() string {
	if a.reserved || !a.history.direct {
		return a.key.ip
	}
	a.history.mu.Lock()
	defer a.history.mu.Unlock()
	return a.peer
}

// Keep active attempts available to subscribers that open details mid-download.
func (s *Server) trackBlockRoute(id useBlockID, a *networkAttempt, release func()) func() {
	s.mu.Lock()
	if s.blockRoutes == nil {
		s.blockRoutes = make(map[useBlockID]*networkAttempt)
	}
	s.blockRoutes[id] = a
	s.mu.Unlock()
	s.notifyBlockObservation()
	return func() {
		s.mu.Lock()
		if s.blockRoutes[id] == a {
			delete(s.blockRoutes, id)
		}
		s.mu.Unlock()
		s.notifyBlockObservation()
		release()
	}
}

func (a *networkAttempt) finish(bytes int64, networkErr error) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		key := a.key
		key.ip = a.currentIP()
		failed := networkErr != nil && !applog.InvalidLocation(networkErr) &&
			!errors.Is(networkErr, upstreamrequest.ErrHeaderBudget) && !errors.Is(networkErr, context.Canceled) &&
			!errors.Is(networkErr, upstreamrequest.ErrCapacity) && !errors.Is(networkErr, upstreamrequest.ErrUnavailable) &&
			!errors.Is(networkErr, upstreamrequest.ErrRetired)
		if errors.Is(context.Cause(a.parent), errSampleTimeout) {
			bytes = 0
			if errors.Is(networkErr, errSampleTimeout) {
				failed = !a.sharedSampleDeadline
			}
		} else if a.parent.Err() != nil {
			bytes, failed = 0, false
		}
		elapsed := time.Since(a.started)
		a.history.record(key, a.source, bytes, failed, elapsed)
		slog.Debug("资源线路尝试结束", "event_name", "resource_route_attempt", "host", key.host, "ip", key.ip, "source", a.source, "bytes", bytes, "network_failure", failed, "duration", elapsed)
	})
}

// Called once by the owner after closing the response (or a failed dial).
func (a *networkAttempt) release() {
	if a == nil {
		return
	}
	a.finish(0, nil)
	if a.reserved {
		a.history.release(a.key)
	} else if a.history.direct {
		h := a.history
		h.mu.Lock()
		a.peerReleased = true
		if a.peer != "" {
			h.entries[routeKey{a.key.host, a.peer}].inFlight--
		}
		// Keep the last peer for the block's retry cursor.
		h.mu.Unlock()
	}
}

func (s *Server) nextResourceAttempt(r *http.Request, attempts *resourceAttempts, c *resourceCandidates) (http.RoundTripper, int, *networkAttempt, bool) {
	h := s.cfg.DownloadRoutes
	if h == nil {
		index, ok := c.attempts.Next()
		if !ok {
			return nil, 0, nil, false
		}
		return c.transports[index], len(c.transports) - index, nil, true
	}
	limit := upstreamrequest.MaxOperationCandidates
	source := "download"
	if attempts.sample != nil {
		limit = 1
		source = "sample"
	}
	if c.used >= limit {
		return nil, 0, nil, false
	}
	a := &networkAttempt{history: h, parent: r.Context(), key: routeKey{host: r.URL.Hostname()}, source: source, sharedSampleDeadline: attempts.redirects > 0}
	if h.direct && s.cfg.DirectCandidates != nil {
		var need time.Time
		if attempts.needBy != nil {
			need = attempts.needBy()
		}
		current := s.cfg.DirectCandidates.Current(r.URL.String())
		if c.tried == nil {
			c.tried = make(map[string]bool)
		}
		if c.ordinary != nil {
			if ip := c.ordinary.currentIP(); ip != "" {
				c.tried[ip] = true
				if c.used == 1 {
					c.soleIP = ip
				}
			}
		}
		tried := c.tried
		if c.used == 1 && c.attempts.RetrySingle && len(current) == 1 && current[0].IP == c.soleIP {
			tried = nil // The sole pinned path gets the existing single body retry.
		}
		selected, count, valid := h.choose(a.key.host, current, tried, need, attempts.sample)
		if !valid && c.used == 0 {
			discovery, cancel := context.WithTimeout(r.Context(), discoveryBudget(r.Context(), s.cfg.DownloadTimeout, attempts.fallbackDeadline))
			current, _ = s.cfg.DirectCandidates.Candidates(discovery, r.URL.String())
			cancel()
			a.sharedSampleDeadline = true
			selected, count, valid = h.choose(a.key.host, current, tried, need, attempts.sample)
		}
		if count > 0 {
			if c.used == 0 && count == 1 {
				c.soleIP = selected.IP
			}
			c.tried[selected.IP] = true
			remaining := min(count, limit-c.used)
			c.used++
			a.key.ip, a.reserved, a.started = selected.IP, true, time.Now()
			return selected.Transport, remaining, a, true
		}
		if valid && attempts.sample == nil { // All valid candidates for this download were tried.
			return nil, 0, nil, false
		}
	}
	if h.direct && attempts.sample != nil {
		c.err = ErrNoSampleDue
		return nil, 0, nil, false
	}
	retryOrdinaryBody := c.used == 1 && c.attempts.RetrySingle && c.ordinary != nil
	if c.ordinary != nil && !retryOrdinaryBody {
		return nil, 0, nil, false
	}
	c.used++
	c.ordinary = a
	a.started = time.Now()
	return s.client.Transport, 1, a, true
}

// Limit cold discovery to one second and leave at least half of the available
// time for the resource request if discovery cannot supply a fixed address.
func discoveryBudget(ctx context.Context, timeout time.Duration, fallbackDeadline func() time.Time) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if fallbackDeadline != nil {
		if left := time.Until(fallbackDeadline()); left > 0 {
			timeout = min(timeout, left)
		}
	}
	return min(time.Second, timeout/2)
}
