//go:build !windows

package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func acquire(string) (Lease, error) {
	return nil, fmt.Errorf("桌面托盘仅支持 Windows，请使用 -no-tray")
}
func portOwner(string, uint16) *Owner { return nil }
func OpenBrowser(url string) error    { return exec.Command("xdg-open", url).Run() }
func ShowError(err error)             { fmt.Fprintln(os.Stderr, err) }
func Run(ctx context.Context, o Options) error {
	if o.Open {
		if err := OpenBrowser(o.URL); err != nil {
			return err
		}
	}
	<-ctx.Done()
	o.Shutdown()
	return nil
}
