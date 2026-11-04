package upstreamnet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// CheckSOCKS5Service negotiates the same methods as the business dialer, then
// closes without sending CONNECT or naming a destination. authenticated is true
// only when the server selected and accepted username/password authentication.
// x/net/proxy exposes only complete destination dials, so this small protocol
// check deliberately stops before that API's CONNECT phase.
func CheckSOCKS5Service(ctx context.Context, address, username, password string) (authenticated bool, err error) {
	if err := ValidateSOCKS5Address(address); err != nil {
		return false, err
	}
	if err := ValidateSOCKS5Credentials(username, password); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return false, fmt.Errorf("SOCKS5 服务连接失败：%w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	greeting := []byte{5, 1, 0}
	if username != "" {
		greeting = []byte{5, 2, 0, 2}
	}
	if _, err := conn.Write(greeting); err != nil {
		return false, fmt.Errorf("SOCKS5 协商失败：%w", err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return false, fmt.Errorf("SOCKS5 协商失败：%w", err)
	}
	if reply[0] != 5 {
		return false, errors.New("服务返回的 SOCKS5 协议版本无效")
	}
	switch reply[1] {
	case 0:
		return false, nil
	case 2:
		if username == "" {
			return false, errors.New("SOCKS5 服务要求未配置的用户名和密码")
		}
	case 255:
		return false, errors.New("SOCKS5 服务拒绝认证方式")
	default:
		return false, errors.New("SOCKS5 服务选择了不支持的认证方式")
	}
	auth := append([]byte{1, byte(len(username))}, username...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, password...)
	if _, err := conn.Write(auth); err != nil {
		return false, fmt.Errorf("SOCKS5 认证失败：%w", err)
	}
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return false, fmt.Errorf("SOCKS5 认证失败：%w", err)
	}
	if reply[0] != 1 {
		return false, errors.New("SOCKS5 认证响应版本无效")
	}
	if reply[1] != 0 {
		return false, errors.New("SOCKS5 用户名或密码未获接受")
	}
	return true, nil
}
