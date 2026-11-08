package desktop

import "fmt"

const perMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)

// The caller must hold its OS thread until restore has run.
func enterPerMonitorDPI() (restore func(), err error) {
	proc := user32.NewProc("SetThreadDpiAwarenessContext")
	if err := proc.Find(); err != nil {
		return nil, err
	}
	previous, _, err := proc.Call(perMonitorV2)
	if previous == 0 {
		return nil, fmt.Errorf("设置 Per-Monitor V2：%w", err)
	}
	return func() { proc.Call(previous) }, nil
}
