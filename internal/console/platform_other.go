//go:build !windows

package console

import "fmt"

func changeHosts(action string) error {
	return fmt.Errorf("一键提权目前仅支持 Windows，请手动配置 hosts")
}
func FlushDNS() {}
