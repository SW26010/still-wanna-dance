package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Direct UDP DNS and IP-bootstrapped DoH never consult OS hosts.
type directDNS struct {
	mu    sync.Mutex
	cache map[string]dnsEntry
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
	d.mu.Lock()
	entry := d.cache[host]
	d.mu.Unlock()
	if time.Now().Before(entry.until) {
		return entry.ips, nil
	}
	var last error
	if ips, ttl, err := queryUDP(ctx, "223.5.5.5:53", host); err == nil {
		d.mu.Lock()
		if d.cache == nil {
			d.cache = map[string]dnsEntry{}
		}
		d.cache[host] = dnsEntry{ips, time.Now().Add(ttl)}
		d.mu.Unlock()
		return ips, nil
	}
	for _, provider := range []struct{ host, ip, path string }{{"dns.alidns.com", "223.5.5.5", "/resolve"}, {"cloudflare-dns.com", "1.1.1.1", "/dns-query"}, {"dns.google", "8.8.8.8", "/resolve"}} {
		ips, ttl, err := queryDNS(ctx, provider.host, provider.ip, provider.path, host)
		if err != nil {
			last = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		d.mu.Lock()
		if d.cache == nil {
			d.cache = map[string]dnsEntry{}
		}
		d.cache[host] = dnsEntry{ips, time.Now().Add(ttl)}
		d.mu.Unlock()
		return ips, nil
	}
	return nil, fmt.Errorf("独立 DNS 解析 %s 失败（不回退到系统 hosts）：%w", host, last)
}

func queryDNS(ctx context.Context, provider, ip, path, host string) ([]string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	t := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	}}
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := http.NewRequestWithContext(ctx, "GET", "https://"+provider+path+"?name="+url.QueryEscape(host)+"&type=A", nil)
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("DNS HTTP %d", resp.StatusCode)
	}
	var answer dnsAnswer
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer); err != nil {
		return nil, 0, err
	}
	return dnsAddresses(answer)
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
	for _, ip := range ips {
		var conn net.Conn
		conn, err = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}
