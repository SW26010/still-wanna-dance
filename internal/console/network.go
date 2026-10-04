package console

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"still-wanna-dance/internal/upstreamrequest"

	"golang.org/x/net/proxy"
)

type upstreamDialFunc func(context.Context, string, string) (net.Conn, error)

// x/net's SOCKS connection embeds net.Conn, which hides TCP half-close.
// Retain the transport per dial, while delegating all other operations to the
// SOCKS connection. Never share this capture across concurrent dials.
type socks5ForwardDialer struct {
	net.Dialer
	tcp *net.TCPConn
}

func (d *socks5ForwardDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		conn.Close()
		return nil, errors.New("SOCKS5 transport is not TCP")
	}
	d.tcp = tcp
	return conn, nil
}

type socks5Conn struct {
	net.Conn
	transport *net.TCPConn
}

func (c *socks5Conn) CloseWrite() error { return c.transport.CloseWrite() }

func validateSOCKS5Address(address string) error {
	host, port, err := net.SplitHostPort(address)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || strings.ContainsAny(host, "/@?# \\\t\r\n") || portErr != nil || n < 1 || n > 65535 {
		return errors.New("SOCKS5 地址必须为主机:端口，例如 127.0.0.1:7891（无需 socks5:// 前缀）")
	}
	return nil
}

// NewSOCKS5Dialer delegates destination DNS and connection establishment to the
// proxy. Only the proxy endpoint is dialed locally; failures never fall back to
// direct DNS or a direct connection to the destination.
func NewSOCKS5Dialer(address string) (func(context.Context, string, string) (net.Conn, error), error) {
	return NewAuthenticatedSOCKS5Dialer(address, "", "")
}

func validateSOCKS5Credentials(username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if len(username) < 1 || len(username) > 255 || len(password) < 1 || len(password) > 255 {
		return errors.New("SOCKS5 用户名和密码须同时填写，各为 1～255 字节；无需认证时均留空")
	}
	return nil
}

// NewAuthenticatedSOCKS5Dialer uses optional RFC 1929 credentials. Empty
// credentials retain unauthenticated SOCKS5; credentials are never in errors.
func NewAuthenticatedSOCKS5Dialer(address, username, password string) (func(context.Context, string, string) (net.Conn, error), error) {
	if err := validateSOCKS5Address(address); err != nil {
		return nil, err
	}
	if err := validateSOCKS5Credentials(username, password); err != nil {
		return nil, err
	}
	var auth *proxy.Auth
	if username != "" {
		auth = &proxy.Auth{User: username, Password: password}
	}
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		forward := &socks5ForwardDialer{Dialer: net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}}
		d, err := proxy.SOCKS5("tcp", address, auth, forward)
		if err != nil {
			return nil, err
		}
		dialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("SOCKS5 dialer does not support context cancellation")
		}
		conn, err := dialer.DialContext(ctx, network, target)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		return &socks5Conn{Conn: conn, transport: forward.tcp}, nil
	}, nil
}

func (c *Console) networkFor(s Settings) (upstreamDialFunc, *http.Client, error) {
	dial := upstreamDialFunc(c.dns.DialContext)
	var proxyDial upstreamrequest.DialFunc
	var proxyID string
	if s.UpstreamMode == "socks5" || s.UpstreamMode == "auto" {
		var err error
		proxyDial, err = NewAuthenticatedSOCKS5Dialer(s.SOCKS5Address, s.SOCKS5Username, s.SOCKS5Password)
		if err != nil {
			return nil, nil, err
		}
		proxyID, err = c.proxyIdentity(s)
		if err != nil {
			return nil, nil, err
		}
		if s.UpstreamMode == "socks5" {
			dial = upstreamDialFunc(proxyDial)
		} else {
			// Legacy raw dial callers have no operation/measurement context.
			// Preserve direct-first fallback; explicit candidates are ranked by Monitor.
			dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := c.dns.DialContext(ctx, network, address)
				if err == nil || ctx.Err() != nil {
					return conn, err
				}
				return proxyDial(ctx, network, address)
			}
		}
	}
	pool, err := upstreamrequest.NewPool(s.UpstreamMode, c.dns, proxyDial, proxyID)
	if err != nil {
		return nil, nil, err
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &upstreamrequest.Transport{Pool: pool, RoundTripper: &http.Transport{
			DialContext: dial, ResponseHeaderTimeout: 20 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return dial, client, nil
}

// Inventory coverage may still be reading while settings are saved. Each
// request keeps an immutable client snapshot; new requests use the new client.
func (c *Console) upstreamClient() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}
