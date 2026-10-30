package upstreamrequest

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Address struct {
	IP      string
	Expires time.Time
}
type DNSPool interface {
	Addresses(context.Context, string) ([]Address, error)
	CurrentAddresses(string) []Address
	DNSChanged() <-chan struct{}
}
type Candidate struct {
	ID, Host, Mode, IP string
	ValidUntil         time.Time
	Transport          http.RoundTripper
}

// MaxOperationCandidates is the shared API/download attempt budget. The caller
// ranks and selects paths before the download layer snapshots this bounded list.
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

// Pool owns fixed-IP direct transports or a single managed proxy endpoint,
// not DNS. A new Pool represents a network configuration.
type Pool struct {
	mu         sync.Mutex
	mode       string
	dns        DNSPool
	proxy      DialFunc
	proxyID    string
	directDial DialFunc
	transports map[string]*http.Transport
	closed     bool
}

var poolSequence atomic.Uint64

// NewDirectPool uses the supplied dialer for every pinned direct connection.
// The pool still owns candidate membership, host checks and connection reuse;
// the dialer receives an IP:port and must not resolve or substitute that IP.
func NewDirectPool(dns DNSPool, dial DialFunc) (*Pool, error) {
	if dial == nil {
		return nil, errors.New("missing direct dialer")
	}
	p, err := NewPool("direct", dns, nil)
	if err == nil {
		p.directDial = dial
	}
	return p, err
}

func NewPool(mode string, dns DNSPool, proxy DialFunc, persistentProxyID ...string) (*Pool, error) {
	if mode != "direct" && mode != "socks5" {
		return nil, errors.New("invalid channel mode")
	}
	if mode != "socks5" && dns == nil || mode != "direct" && proxy == nil {
		return nil, errors.New("missing channel dependency")
	}
	id := strconv.FormatUint(poolSequence.Add(1), 10)
	if len(persistentProxyID) > 0 && persistentProxyID[0] != "" {
		if !PersistentProxyID(persistentProxyID[0]) {
			return nil, errors.New("invalid persistent proxy identity")
		}
		id = persistentProxyID[0]
	}
	return &Pool{mode: mode, dns: dns, proxy: proxy, proxyID: id, transports: make(map[string]*http.Transport)}, nil
}

// PersistentProxyID identifies a versioned, keyed configuration fingerprint.
// Numeric pool sequence identities are never safe to restore across processes.
func PersistentProxyID(id string) bool {
	if !strings.HasPrefix(id, "stable-v1-") {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(id, "stable-v1-"))
	return err == nil && len(b) == 32
}
func targetHost(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
func (p *Pool) Candidates(ctx context.Context, target string) ([]Candidate, error) {
	host := targetHost(target)
	if host == "" {
		return nil, errors.New("invalid channel target")
	}
	var err error
	if p.mode == "direct" {
		_, err = p.dns.Addresses(ctx, host)
	}
	return p.Current(target), err
}
func (p *Pool) Changed() <-chan struct{} {
	if p.mode == "socks5" {
		return nil
	}
	return p.dns.DNSChanged()
}
func (p *Pool) Current(target string) []Candidate {
	host := targetHost(target)
	if host == "" {
		return nil
	}
	var cs []Candidate
	if p.mode != "socks5" {
		var v4, v6 []Candidate
		for _, a := range p.dns.CurrentAddresses(host) {
			if time.Now().Before(a.Expires) {
				c := Candidate{ID: "direct/" + host + "/" + a.IP, Host: host, Mode: "direct", IP: a.IP, ValidUntil: a.Expires}
				if net.ParseIP(a.IP).To4() != nil {
					v4 = append(v4, c)
				} else {
					v6 = append(v6, c)
				}
			}
		}
		// Cold fixed-IP requests cover both families before exhausting one.
		// Measured order is still supplied by Monitor, never by DNS or dialing.
		for i := 0; i < max(len(v4), len(v6)); i++ {
			if i < len(v6) {
				cs = append(cs, v6[i])
			}
			if i < len(v4) {
				cs = append(cs, v4[i])
			}
		}
	}
	if p.mode != "direct" {
		cs = append(cs, Candidate{ID: "socks5/" + host + "/" + p.proxyID, Host: host, Mode: "socks5"})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	// Reclaim removed IP pools for this host without closing in-flight streams.
	for id, tr := range p.transports {
		if strings.Contains(id, "/"+host+"/") && !slices.ContainsFunc(cs, func(c Candidate) bool { return c.ID == id }) {
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
				if err := p.ensureUsable(ctx, c); err != nil {
					return nil, err
				}
				if c.Mode == "socks5" {
					return p.proxy(ctx, network, address)
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
func (p *Pool) usable(c Candidate) bool {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return false
	}
	if c.Mode == "socks5" {
		return true
	}
	return slices.ContainsFunc(p.dns.CurrentAddresses(c.Host), func(a Address) bool { return a.IP == c.IP && time.Now().Before(a.Expires) })
}

// Long-running callers can outlive a DNS answer, especially with short TTLs.
// Revalidate the same IP through the shared resolver; never change the pinned
// route or let a failed lookup/retired configuration revive an expired address.
func (p *Pool) ensureUsable(ctx context.Context, c Candidate) error {
	if p.usable(c) {
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
	if err != nil || !p.usable(c) {
		return ErrUnavailable
	}
	return nil
}

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
	if err := t.pool.ensureUsable(r.Context(), t.candidate); err != nil {
		return nil, err
	}
	return t.transport.RoundTrip(r)
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
