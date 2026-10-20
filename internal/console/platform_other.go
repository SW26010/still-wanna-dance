//go:build !windows

package console

import (
	"context"
	"fmt"
)

func launchHostsSession(context.Context) (hostsConnection, error) {
	return nil, fmt.Errorf("一键提权目前仅支持 Windows，请手动配置 hosts")
}
func RunHostsSession(string, uint32) error { return fmt.Errorf("hosts 助手仅支持 Windows") }
func FlushDNS()                            {}
