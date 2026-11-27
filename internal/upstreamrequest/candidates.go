package upstreamrequest

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type Address struct {
	IP      string
	Expires time.Time           // Zero for a preset address without a DNS lease.
	Allows  func(*url.URL) bool `json:"-"` // Nil when eligible for every target on the host.
}
type DNSPool interface {
	Addresses(context.Context, string) ([]Address, error)
	CurrentAddresses(string) []Address
	DNSChanged() <-chan struct{}
}

// A resolver can optionally observe fixed-IP requests to refresh DNS off-path.
type candidateUseObserver interface {
	NoteCandidateUse(host string)
}
type Candidate struct {
	ID, Host, Mode, IP string
	ValidUntil         time.Time
	Transport          http.RoundTripper
}

// MaxOperationCandidates bounds metadata requests and one-shot diagnostics.
const MaxOperationCandidates = 6

// Candidates may populate cold DNS state. A discovery error may accompany still
// valid cached candidates; it must not authorize pruning undiscovered history.
// Current and Changed never perform I/O.
type Candidates interface {
	Candidates(context.Context, string) ([]Candidate, error)
	Current(string) []Candidate
	Changed() <-chan struct{}
}

type DialFunc func(context.Context, string, string) (net.Conn, error)

// Pool owns fixed-IP direct transports, not DNS. Proxies use ordinary clients.
type Pool struct {
	mu         sync.Mutex
	dns        DNSPool
	directDial DialFunc
	transports map[string]*http.Transport
	closed     bool
}

// NewDirectPool uses the supplied dialer for every pinned direct connection.
// The pool still owns candidate membership, host checks and connection reuse;
// the dialer receives an IP:port and must not resolve or substitute that IP.
func NewDirectPool(dns DNSPool, dial DialFunc) (*Pool, error) {
	if dial == nil {
		return nil, errors.New("missing direct dialer")
	}
	p, err := NewPool(dns)
	if err == nil {
		p.directDial = dial
	}
	return p, err
}

func NewPool(dns DNSPool) (*Pool, error) {
	if dns == nil {
		return nil, errors.New("missing direct DNS resolver")
	}
	return &Pool{dns: dns, transports: make(map[string]*http.Transport)}, nil
}
func targetEndpoint(target string) *url.URL {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	return u
}

func requestPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (p *Pool) Candidates(ctx context.Context, target string) ([]Candidate, error) {
	u := targetEndpoint(target)
	if u == nil {
		return nil, errors.New("invalid channel target")
	}
	_, err := p.dns.Addresses(ctx, strings.ToLower(u.Hostname()))
	return p.current(u), err
}
func (p *Pool) Changed() <-chan struct{} {
	return p.dns.DNSChanged()
}
func (p *Pool) Current(target string) []Candidate {
	u := targetEndpoint(target)
	if u == nil {
		return nil
	}
	return p.current(u)
}

// OrderByFamily interleaves candidates in place, starting with IPv6.
// When sampling a subset, shuffle peers within each family first.
func OrderByFamily(cs []Candidate, shuffle bool) {
	var v6, v4 []Candidate
	for _, c := range cs {
		if net.ParseIP(c.IP).To4() != nil {
			v4 = append(v4, c)
		} else {
			v6 = append(v6, c)
		}
	}
	if shuffle {
		for _, family := range [][]Candidate{v6, v4} {
			rand.Shuffle(len(family), func(i, j int) { family[i], family[j] = family[j], family[i] })
		}
	}
	ordered := cs[:0]
	for i := 0; i < max(len(v6), len(v4)); i++ {
		if i < len(v6) {
			ordered = append(ordered, v6[i])
		}
		if i < len(v4) {
			ordered = append(ordered, v4[i])
		}
	}
}

func (p *Pool) current(u *url.URL) []Candidate {
	host := strings.ToLower(u.Hostname())
	addresses := p.dns.CurrentAddresses(host)
	now := time.Now()
	prefix := "direct/" + host + "/"
	live := make(map[string]bool, len(addresses))
	var cs []Candidate
	for _, a := range addresses {
		if !a.valid(now) {
			continue
		}
		id := prefix + a.IP
		live[id] = true // An address for another endpoint still owns its transport.
		if a.allowsTarget(u) {
			cs = append(cs, Candidate{ID: id, Host: host, Mode: "direct", IP: a.IP, ValidUntil: a.Expires})
		}
	}
	OrderByFamily(cs, false)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	// Reclaim removed IP pools for this host without closing in-flight streams.
	for id, tr := range p.transports {
		if strings.HasPrefix(id, prefix) && !live[id] {
			tr.CloseIdleConnections()
			delete(p.transports, id)
		}
	}
	for i := range cs {
		c := cs[i]
		tr := p.transports[c.ID]
		if tr == nil {
			tr = &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				h, port, err := net.SplitHostPort(address)
				if err != nil || !strings.EqualFold(h, c.Host) {
					return nil, errors.New("channel host mismatch")
				}
				target, ok := ctx.Value(candidateTargetKey{}).(*url.URL)
				if !ok || requestPort(target) != port {
					return nil, ErrUnavailable
				}
				if err := p.ensureUsable(ctx, c, target); err != nil {
					return nil, err
				}
				if p.directDial != nil {
					return p.directDial(ctx, network, net.JoinHostPort(c.IP, port))
				}
				return (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(c.IP, port))
			}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 90 * time.Second}
			p.transports[c.ID] = tr
		}
		cs[i].Transport = &candidateTransport{pool: p, candidate: c, transport: tr}
	}
	return cs
}

func (a Address) valid(now time.Time) bool {
	return a.Expires.IsZero() || now.Before(a.Expires)
}

func (a Address) allowsTarget(u *url.URL) bool {
	return a.Allows == nil || a.Allows(u)
}

func (p *Pool) usable(c Candidate, u *url.URL) bool {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return false
	}
	now := time.Now()
	return slices.ContainsFunc(p.dns.CurrentAddresses(c.Host), func(a Address) bool {
		return a.IP == c.IP && a.valid(now) && a.allowsTarget(u)
	})
}

// Revalidate the same IP through the shared resolver. A failed lookup does not
// renew an expired DNS lease. A preset IP can still be available.
func (p *Pool) ensureUsable(ctx context.Context, c Candidate, u *url.URL) error {
	if p.usable(c, u) {
		return nil
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || c.Mode != "direct" {
		return ErrUnavailable
	}
	_, err := p.dns.Addresses(ctx, c.Host)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !p.usable(c, u) {
		return ErrUnavailable
	}
	return nil
}

// The shared host/IP transport rechecks the actual request target when dialing.
type candidateTargetKey struct{}

type candidateTransport struct {
	pool      *Pool
	candidate Candidate
	transport *http.Transport
}

func (t *candidateTransport) ChannelID() string { return t.candidate.ID }

func (t *candidateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.EqualFold(r.URL.Hostname(), t.candidate.Host) {
		return nil, ErrUnavailable
	}
	if err := t.pool.ensureUsable(r.Context(), t.candidate, r.URL); err != nil {
		return nil, err
	}
	if observer, ok := t.pool.dns.(candidateUseObserver); ok && r.Context().Err() == nil {
		observer.NoteCandidateUse(t.candidate.Host)
	}
	return t.transport.RoundTrip(r.WithContext(context.WithValue(r.Context(), candidateTargetKey{}, r.URL)))
}
func (p *Pool) CloseIdleConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, tr := range p.transports {
		tr.CloseIdleConnections()
	}
}
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.CloseIdleConnections()
}

// Transport preserves ordinary requests while exposing explicit candidates to
// measurement consumers. Closing the configuration invalidates saved candidates.
type Transport struct {
	http.RoundTripper
	*Pool
}

func (t *Transport) CloseIdleConnections() {
	if c, ok := t.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
	t.Pool.CloseIdleConnections()
}
func (t *Transport) Retire() { t.Pool.Close(); t.CloseIdleConnections() }
