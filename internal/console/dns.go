package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
)

// Certificate-verified DoH never consults OS hosts or plaintext DNS.
type directDNS struct {
	mu            sync.Mutex
	cache         map[string]dnsEntry
	lookups       map[string]*dnsLookup
	bootstrapping map[string]chan struct{}
	states        map[string]*dnsState
	policy        DNSPolicy
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	closed        bool
	paused        bool
}

type dnsLookup struct {
	done chan struct{}
	ips  []string
	err  error
}

// NewUpstreamDialer shares the desktop's certificate-verified DoH implementation
// with the command-line service. It never falls back to the system resolver.
func NewUpstreamDialer() func(context.Context, string, string) (net.Conn, error) {
	d := &directDNS{}
	return d.DialContext
}

type dnsEntry struct {
	ips   []string
	until time.Time
}
type dnsAnswer struct {
	Status int
	Answer []struct {
		Type int
		TTL  int
		Data string
	}
}

func (d *directDNS) lookup(ctx context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	return d.lookupShared(ctx, host, func(ctx context.Context) ([]string, time.Duration, error) {
		return d.resolve(ctx, host)
	})
}

// Cache checks and in-flight registration are atomic. Each caller can stop
// waiting independently; the shared query retains its own configured timeout.
func (d *directDNS) lookupShared(ctx context.Context, host string, query dnsQuery) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, errors.New("DNS resolver closed")
	}
	d.initLocked()
	s := d.states[host]
	if s == nil {
		s = &dnsState{query: query, attempted: time.Now()}
		d.states[host] = s
	}
	s.query = query
	s.lastUsed = time.Now()
	if entry := d.cache[host]; time.Now().Before(entry.until) {
		d.scheduleLocked(host, s)
		d.mu.Unlock()
		return slices.Clone(entry.ips), nil
	}
	call := d.queryLocked(ctx, host, s)
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.done:
		return slices.Clone(call.ips), call.err
	}
}

func (d *directDNS) resolve(ctx context.Context, host string) ([]string, time.Duration, error) {
	var queries []dnsQuery
	for _, provider := range dohProviders {
		queries = append(queries, func(ctx context.Context) ([]string, time.Duration, error) {
			return d.queryProvider(ctx, provider, host)
		})
	}
	ips, ttl, err := collectDNS(ctx, queries)
	if err != nil {
		return nil, 0, fmt.Errorf("独立 DNS 解析 %s 失败（不回退到系统 hosts）：%w", host, err)
	}
	return ips, ttl, nil
}

type dnsQuery func(context.Context) ([]string, time.Duration, error)

// Collect a small pool, allowing 200ms after the first usable answer for other
// providers. Unreachable public resolvers cannot delay a working answer by seconds.
func collectDNS(ctx context.Context, queries []dnsQuery) ([]string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	type result struct {
		ips []string
		ttl time.Duration
		err error
	}
	results := make(chan result, len(queries))
	for _, query := range queries {
		go func(query dnsQuery) { ips, ttl, err := query(ctx); results <- result{ips, ttl, err} }(query)
	}
	var ips []string
	ttl := 5 * time.Minute
	seen := map[string]bool{}
	var failures []error
	var settle <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for range queries {
		select {
		case <-ctx.Done():
			if len(ips) > 0 {
				return ips, ttl, nil
			}
			return nil, 0, ctx.Err()
		case <-settle:
			return ips, ttl, nil
		case r := <-results:
			if r.err != nil {
				failures = append(failures, r.err)
				continue
			}
			if len(r.ips) == 0 {
				continue
			}
			ttl = min(ttl, r.ttl)
			for _, ip := range r.ips {
				if !seen[ip] && len(ips) < 6 {
					seen[ip] = true
					ips = append(ips, ip)
				}
			}
			if timer == nil {
				timer = time.NewTimer(200 * time.Millisecond)
				settle = timer.C
			}
		}
	}
	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("no usable DNS answers: %w", errors.Join(failures...))
	}
	return ips, ttl, nil
}

func queryDNS(ctx context.Context, provider, ip, path, host string) ([]string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	t := newDoHTransport(net.JoinHostPort(ip, "443"))
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return exchangeDoH(ctx, client, "https://"+provider+path, host)
}

// Dial only the supplied IP; URL hostname still supplies TLS SNI and certificate
// verification. No proxy, system DNS, custom trust roots or insecure TLS flags.
func newDoHTransport(address string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}}
}

func exchangeDoH(ctx context.Context, client *http.Client, endpoint, host string) ([]string, time.Duration, error) {
	packet, err := newDNSQuery(host)
	if err != nil {
		return nil, 0, err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(packet))
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Accept", "application/dns-message")
	r.Header.Set("Content-Type", "application/dns-message")
	resp, err := client.Do(r)
	if err != nil {
		return nil, 0, applog.SafeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("DNS HTTP %d", resp.StatusCode)
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/dns-message" {
		return nil, 0, fmt.Errorf("invalid DoH content type")
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, 0, err
	}
	if len(reply) > 65535 {
		return nil, 0, fmt.Errorf("oversized DoH response")
	}
	return parseDNSReply(packet, reply)
}

func dnsAddresses(answer dnsAnswer) ([]string, time.Duration, error) {
	if answer.Status != 0 {
		return nil, 0, fmt.Errorf("DNS status %d", answer.Status)
	}
	ips := []string{}
	ttl := 300
	for _, a := range answer.Answer {
		// An address is usable only while every alias leading to it remains valid.
		if a.Type == 5 {
			if a.TTL < ttl {
				ttl = a.TTL
			}
			continue
		}
		if a.Type != 1 {
			continue
		}
		ip := net.ParseIP(a.Data)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsPrivate() {
			continue
		}
		ips = append(ips, ip.String())
		if a.TTL < ttl {
			ttl = a.TTL
		}
	}
	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("DNS 未返回可用的公网 IPv4 地址")
	}
	if ttl < 0 {
		ttl = 0
	}
	return ips, time.Duration(ttl) * time.Second, nil
}

func (d *directDNS) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
		ip   string
	}
	results := make(chan result)
	for i, ip := range ips {
		go func(i int, ip string) {
			timer := time.NewTimer(time.Duration(i) * 200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			conn, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
			select {
			case results <- result{conn, err, ip}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}(i, ip)
	}
	var failures []error
	for range ips {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			if r.err == nil {
				d.mu.Lock()
				entry := d.cache[host]
				if slices.Contains(entry.ips, r.ip) {
					ordered := []string{r.ip}
					for _, ip := range entry.ips {
						if ip != r.ip {
							ordered = append(ordered, ip)
						}
					}
					entry.ips = ordered
					d.cache[host] = entry
				}
				d.mu.Unlock()
				return r.conn, nil
			}
			failures = append(failures, r.err)
		}
	}
	d.mu.Lock()
	delete(d.cache, host)
	d.mu.Unlock()
	return nil, errors.Join(failures...)
}
