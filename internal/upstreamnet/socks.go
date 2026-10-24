package upstreamnet

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

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

func ValidateSOCKS5Address(address string) error {
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

func ValidateSOCKS5Credentials(username, password string) error {
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
	if err := ValidateSOCKS5Address(address); err != nil {
		return nil, err
	}
	if err := ValidateSOCKS5Credentials(username, password); err != nil {
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
