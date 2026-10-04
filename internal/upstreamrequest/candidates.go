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

// Candidates may populate cold DNS state. Current and Changed never perform I/O.
type Candidates interface {
	Candidates(context.Context, string) ([]Candidate, error)
	Current(string) []Candidate
	Changed() <-chan struct{}
}

// Readiness describes authoritative membership, not mere path availability.
// A disabled mode is ready too: its candidate set is deliberately empty.
type CandidateReadiness struct{ Direct, Proxy bool }
type CandidateDiscovery interface {
	CandidatesWithReadiness(context.Context, string) ([]Candidate, CandidateReadiness, error)
}
type DialFunc func(context.Context, string, string) (net.Conn, error)

// Pool owns pinned transports, not DNS. A new Pool represents a network config.
type Pool struct {
	mu         sync.Mutex
	mode       string
	dns        DNSPool
	proxy      DialFunc
	transports map[string]*http.Transport
	closed     bool
	proxyID    string
}

var poolSequence atomic.Uint64

func NewPool(mode string, dns DNSPool, proxy DialFunc, persistentProxyID ...string) (*Pool, error) {
	if mode != "direct" && mode != "socks5" && mode != "auto" {
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
	return &Pool{mode: mode, dns: dns, proxy: proxy, transports: make(map[string]*http.Transport), proxyID: id}, nil
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
	cs, _, err := p.CandidatesWithReadiness(ctx, target)
	return cs, err
}

func (p *Pool) CandidatesWithReadiness(ctx context.Context, target string) ([]Candidate, CandidateReadiness, error) {
	host := targetHost(target)
	if host == "" {
		return nil, CandidateReadiness{}, errors.New("invalid channel target")
	}
	ready := CandidateReadiness{Direct: p.mode == "socks5", Proxy: true}
	var err error
	if p.mode != "socks5" {
		_, err = p.dns.Addresses(ctx, host)
		ready.Direct = err == nil && ctx.Err() == nil
	}
	cs := p.Current(target)
	if len(cs) > 0 {
		return cs, ready, nil
	}
	return nil, ready, err
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
		for _, a := range p.dns.CurrentAddresses(host) {
			if time.Now().Before(a.Expires) {
				cs = append(cs, Candidate{ID: "direct/" + host + "/" + a.IP, Host: host, Mode: "direct", IP: a.IP, ValidUntil: a.Expires})
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
				if !p.usable(c) {
					return nil, ErrUnavailable
				}
				if c.Mode == "socks5" {
					return p.proxy(ctx, network, address)
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

type candidateTransport struct {
	pool      *Pool
	candidate Candidate
	transport *http.Transport
}

func (t *candidateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.EqualFold(r.URL.Hostname(), t.candidate.Host) || !t.pool.usable(t.candidate) {
		return nil, ErrUnavailable
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
