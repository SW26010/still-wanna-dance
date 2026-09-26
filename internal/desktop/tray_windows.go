package desktop

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var shell32 = syscall.NewLazyDLL("shell32.dll")

func wide(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func OpenBrowser(url string) error {
	r, _, err := shell32.NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(wide("open"))), uintptr(unsafe.Pointer(wide(url))), 0, 0, 1)
	if r <= 32 {
		return fmt.Errorf("打开控制台失败 (%d): %v", r, err)
	}
	return nil
}

func ShowError(err error) {
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(wide(err.Error()))), uintptr(unsafe.Pointer(wide("StepStash"))), 0x10)
}

type point struct{ X, Y int32 }
type windowClass struct {
	Style                              uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
}
type message struct {
	Window         uintptr
	ID             uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type notifyIcon struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}

const (
	wmClose    = 0x10
	wmCommand  = 0x111
	wmTimer    = 0x113
	wmTray     = 0x8001
	wmFinished = 0x8002
)

type tray struct {
	options      Options
	hwnd, icon   uintptr
	added        bool
	closing      bool // accessed only by the window's OS thread
	busy         atomic.Bool
	taskbar      uint32
	tip          string
	finished     chan struct{}
	shutdownDone chan struct{}
}

// Run keeps all window and icon operations on one OS thread. Slow service
// operations execute separately, so the tray remains responsive during downloads.
func Run(ctx context.Context, o Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if proc := user32.NewProc("SetThreadDpiAwarenessContext"); proc.Find() == nil {
		old, _, _ := proc.Call(^uintptr(3))
		defer proc.Call(old)
	}
	t := &tray{options: o, finished: make(chan struct{}), shutdownDone: make(chan struct{})}
	defer close(t.finished)
	instance, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	className := wide(fmt.Sprintf("StepStash.Tray.%d", os.Getpid()))
	callback := syscall.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) (result uintptr) {
		defer func() {
			if problem := recover(); problem != nil {
				ShowError(fmt.Errorf("托盘操作失败：%v", problem))
			}
		}()
		return t.dispatch(hwnd, msg, w, l)
	})
	wc := windowClass{Proc: callback, Instance: instance, Name: className}
	if r, _, err := user32.NewProc("RegisterClassW").Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("注册托盘窗口：%w", err)
	}
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(className)), instance)
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(wide("StepStash"))), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建托盘窗口：%w", err)
	}
	t.hwnd = hwnd
	defer user32.NewProc("DestroyWindow").Call(hwnd)
	t.icon, err = makeIcon()
	if err != nil {
		return err
	}
	defer user32.NewProc("DestroyIcon").Call(t.icon)
	taskbar, _, _ := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(wide("TaskbarCreated"))))
	t.taskbar = uint32(taskbar)
	if err = t.addIcon(); err != nil {
		return err
	}
	defer t.removeIcon()
	user32.NewProc("SetTimer").Call(hwnd, 1, 1000, 0)
	defer user32.NewProc("KillTimer").Call(hwnd, 1)
	go func() {
		select {
		case <-ctx.Done():
			user32.NewProc("PostMessageW").Call(hwnd, wmClose, 0, 0)
		case <-t.finished:
		}
	}()
	if o.Open {
		go func() {
			if err := OpenBrowser(o.URL); err != nil {
				ShowError(err)
			}
		}()
	}
	var msg message
	for {
		r, _, err := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) == -1 {
			return fmt.Errorf("托盘消息循环：%w", err)
		}
		if r == 0 {
			return nil
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (t *tray) iconData() notifyIcon {
	n := notifyIcon{Window: t.hwnd, ID: 1, Flags: 1 | 2 | 4 | 0x80, Callback: wmTray, Icon: t.icon, Version: 4}
	n.Size = uint32(unsafe.Sizeof(n))
	copy(n.Tip[:], syscall.StringToUTF16(t.tip))
	return n
}
func (t *tray) addIcon() error {
	t.tip = t.tooltip()
	n := t.iconData()
	if r, _, err := shell32.NewProc("Shell_NotifyIconW").Call(0, uintptr(unsafe.Pointer(&n))); r == 0 {
		return fmt.Errorf("创建托盘图标：%w", err)
	}
	t.added = true
	if r, _, err := shell32.NewProc("Shell_NotifyIconW").Call(4, uintptr(unsafe.Pointer(&n))); r == 0 {
		t.removeIcon()
		return fmt.Errorf("设置托盘交互：%w", err)
	}
	return nil
}
func (t *tray) removeIcon() {
	if t.added {
		n := t.iconData()
		shell32.NewProc("Shell_NotifyIconW").Call(2, uintptr(unsafe.Pointer(&n)))
		t.added = false
	}
}
func (t *tray) tooltip() string {
	if t.closing {
		return "StepStash · 正在退出"
	}
	s := t.options.State()
	text := "StepStash · CDN 已关闭"
	if s.CDN {
		text = "StepStash · CDN 运行中"
	}
	if s.Batch {
		text += " · 批量任务运行中"
	}
	return text
}
func (t *tray) updateTip() {
	next := t.tooltip()
	if next == t.tip {
		return
	}
	t.tip = next
	n := t.iconData()
	n.Flags = 4 | 0x80
	shell32.NewProc("Shell_NotifyIconW").Call(1, uintptr(unsafe.Pointer(&n)))
}

func (t *tray) quit() {
	if t.closing {
		return
	}
	t.closing = true
	t.updateTip()
	go func() {
		t.options.Shutdown()
		close(t.shutdownDone)
		user32.NewProc("PostMessageW").Call(t.hwnd, wmFinished, 0, 0)
	}()
}

func (t *tray) dispatch(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
	if t.taskbar != 0 && msg == t.taskbar {
		t.added = false
		if err := t.addIcon(); err != nil {
			ShowError(err)
		}
		return 0
	}
	switch msg {
	case 0x11:
		return 1 // WM_QUERYENDSESSION: do not shut down if logoff is later canceled.
	case 0x16: // WM_ENDSESSION: bound the OS callback while normal cleanup runs.
		if w != 0 {
			t.quit()
			select {
			case <-t.shutdownDone:
			case <-time.After(4 * time.Second):
			}
			t.removeIcon()
			user32.NewProc("PostQuitMessage").Call(0)
		}
		return 0
	case wmClose:
		t.quit()
		return 0
	case wmFinished:
		t.removeIcon()
		user32.NewProc("DestroyWindow").Call(hwnd)
		return 0
	case 2:
		t.removeIcon()
		user32.NewProc("PostQuitMessage").Call(0)
		return 0
	case wmTimer:
		if !t.closing {
			t.updateTip()
		}
		return 0
	case wmTray:
		if t.closing {
			return 0
		}
		switch l & 0xffff {
		case 0x400, 0x401, 0x203:
			t.command(OpenConsole)
		case 0x7b:
			t.showMenu()
		}
		return 0
	case wmCommand:
		t.command(int(w & 0xffff))
		return 0
	}
	r, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), w, l)
	return r
}

func (t *tray) command(id int) {
	if t.closing {
		return
	}
	if id == Exit {
		t.quit()
		return
	}
	if id == OpenConsole {
		go func() {
			if err := OpenBrowser(t.options.URL); err != nil {
				ShowError(err)
			}
		}()
		return
	}
	if !t.busy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer t.busy.Store(false)
		if err := t.options.Command(id); err != nil {
			ShowError(err)
		}
	}()
}

func (t *tray) showMenu() {
	menu, _, _ := user32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		return
	}
	defer user32.NewProc("DestroyMenu").Call(menu)
	for _, item := range Menu(t.options.State(), t.busy.Load()) {
		flags := uintptr(0)
		if item.ID == 0 {
			flags = 0x800
		} else if item.Disabled {
			flags = 1
		}
		user32.NewProc("AppendMenuW").Call(menu, flags, uintptr(item.ID), uintptr(unsafe.Pointer(wide(item.Label))))
	}
	var p point
	user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&p)))
	user32.NewProc("SetForegroundWindow").Call(t.hwnd)
	id, _, _ := user32.NewProc("TrackPopupMenu").Call(menu, 0x102, uintptr(p.X), uintptr(p.Y), 0, t.hwnd, 0)
	user32.NewProc("PostMessageW").Call(t.hwnd, 0, 0, 0)
	if id != 0 {
		t.command(int(id))
	}
}

func makeIcon() (uintptr, error) {
	const size = 32
	mask := make([]byte, size*size/8)
	pixels := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := ((size-1-y)*size + x) * 4
			corner := (x < 4 || x >= size-4) && (y < 4 || y >= size-4)
			if corner {
				mask[(size-1-y)*4+x/8] |= 1 << uint(7-x%8)
				continue
			}
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 152, 237, 176, 255
			if (x >= 9 && x <= 12 && y >= 8 && y <= 21) || (y >= 19 && y <= 22 && x >= 9 && x <= 23) || (x >= 20 && x <= 23 && y >= 13 && y <= 22) {
				pixels[i], pixels[i+1], pixels[i+2] = 22, 32, 20
			}
		}
	}
	h, _, err := user32.NewProc("CreateIcon").Call(0, size, size, 1, 32, uintptr(unsafe.Pointer(&mask[0])), uintptr(unsafe.Pointer(&pixels[0])))
	if h == 0 {
		return 0, fmt.Errorf("创建图标：%w", err)
	}
	return h, nil
}
