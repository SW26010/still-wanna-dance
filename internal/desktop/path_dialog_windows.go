package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IFileDialog / IShellItem, in Windows SDK vtable order (shobjidl_core.h).
// Only these two Shell interfaces are needed; no automation/IDispatch layer.
type fileDialog struct{ vtbl *fileDialogVtbl }
type fileDialogVtbl struct {
	QueryInterface, AddRef, Release                                    uintptr
	Show                                                               uintptr // IModalWindow
	SetFileTypes, SetFileTypeIndex, GetFileTypeIndex, Advise, Unadvise uintptr
	SetOptions, GetOptions, SetDefaultFolder, SetFolder, GetFolder     uintptr
	GetCurrentSelection, SetFileName, GetFileName, SetTitle            uintptr
	SetOkButtonLabel, SetFileNameLabel, GetResult, AddPlace            uintptr
	SetDefaultExtension, Close, SetClientGuid, ClearClientData         uintptr
	SetFilter                                                          uintptr
}
type shellItem struct{ vtbl *shellItemVtbl }
type shellItemVtbl struct {
	QueryInterface, AddRef, Release, BindToHandler, GetParent uintptr
	GetDisplayName, GetAttributes, Compare                    uintptr
}

var (
	fileOpenCLSID = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	fileSaveCLSID = windows.GUID{Data1: 0xc0b4e2f3, Data2: 0xba21, Data3: 0x4773, Data4: [8]byte{0x8d, 0xba, 0x33, 0x5e, 0xc9, 0x46, 0xeb, 0x8b}}
	fileDialogIID = windows.GUID{Data1: 0x42f85136, Data2: 0xdb7e, Data3: 0x439c, Data4: [8]byte{0x85, 0xf1, 0xe4, 0x07, 0x5d, 0x13, 0x5f, 0xc8}}
	shellItemIID  = windows.GUID{Data1: 0x43826d1e, Data2: 0xe718, Data3: 0x42ee, Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
)

const (
	fosOverwritePrompt  = 0x2
	fosNoChangeDir      = 0x8
	fosPickFolders      = 0x20
	fosForceFilesystem  = 0x40
	fosPathMustExist    = 0x800
	fosFileMustExist    = 0x1000
	fosNoTestFileCreate = 0x10000
	fosDontAddToRecent  = 0x02000000
	dialogCanceled      = 0x800704c7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
)

func showPathDialog(request PathDialog) (string, error) {
	// COM initialization, owner window, modal UI and all releases share one STA.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restoreDPI, err := enterPerMonitorDPI()
	if err != nil {
		return "", err
	}
	defer restoreDPI()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
		return "", err
	}
	defer windows.CoUninitialize()
	owner, cleanup, err := newPathDialogOwner()
	if err != nil {
		return "", err
	}
	defer cleanup()
	dialog, err := newFileDialog(request)
	if err != nil {
		return "", err
	}
	defer syscall.SyscallN(dialog.vtbl.Release, uintptr(unsafe.Pointer(dialog)))
	hr, _, _ := syscall.SyscallN(dialog.vtbl.Show, uintptr(unsafe.Pointer(dialog)), owner)
	if uint32(hr) == dialogCanceled {
		return "", nil
	}
	if err := pathDialogHRESULT("IFileDialog.Show", hr); err != nil {
		return "", err
	}
	var result *shellItem
	if err := dialog.call(dialog.vtbl.GetResult, uintptr(unsafe.Pointer(&result))); err != nil {
		return "", err
	}
	defer syscall.SyscallN(result.vtbl.Release, uintptr(unsafe.Pointer(result)))
	var path *uint16
	hr, _, _ = syscall.SyscallN(result.vtbl.GetDisplayName, uintptr(unsafe.Pointer(result)), 0x80058000, uintptr(unsafe.Pointer(&path))) // SIGDN_FILESYSPATH
	if err := pathDialogHRESULT("IShellItem.GetDisplayName", hr); err != nil {
		return "", err
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(path))
	return windows.UTF16PtrToString(path), nil
}

func newFileDialog(request PathDialog) (_ *fileDialog, err error) {
	class := &fileOpenCLSID
	if request.Mode == "database" {
		class = &fileSaveCLSID
	}
	var dialog *fileDialog
	hr, _, _ := windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance").Call(
		uintptr(unsafe.Pointer(class)), 0, 1, uintptr(unsafe.Pointer(&fileDialogIID)), uintptr(unsafe.Pointer(&dialog))) // CLSCTX_INPROC_SERVER
	if err := pathDialogHRESULT("CoCreateInstance(IFileDialog)", hr); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			syscall.SyscallN(dialog.vtbl.Release, uintptr(unsafe.Pointer(dialog)))
		}
	}()
	var options uint32
	if err = dialog.call(dialog.vtbl.GetOptions, uintptr(unsafe.Pointer(&options))); err != nil {
		return nil, err
	}
	options |= fosForceFilesystem | fosPathMustExist | fosNoChangeDir | fosDontAddToRecent | fosNoTestFileCreate
	switch request.Mode {
	case "folder":
		options |= fosPickFolders
	case "open":
		options |= fosFileMustExist
	case "database":
		// Selecting a database never creates, probes or overwrites the file.
		options &^= fosFileMustExist | fosOverwritePrompt
	}
	if err = dialog.call(dialog.vtbl.SetOptions, uintptr(options)); err != nil {
		return nil, err
	}
	if err = dialog.setText(dialog.vtbl.SetTitle, request.Title); err != nil {
		return nil, err
	}
	if request.Mode == "database" {
		if err = dialog.setText(dialog.vtbl.SetDefaultExtension, "sqlite"); err != nil {
			return nil, err
		}
		if err = dialog.setText(dialog.vtbl.SetOkButtonLabel, "选择"); err != nil {
			return nil, err
		}
	}
	initial := request.Initial
	if initial != "" && request.Mode != "folder" && !pathDialogDirectory(initial) {
		if err = dialog.setText(dialog.vtbl.SetFileName, filepath.Base(initial)); err != nil {
			return nil, err
		}
		initial = filepath.Dir(initial)
	}
	// A moved/unavailable location starts at its nearest existing parent.
	for initial != "" && !pathDialogDirectory(initial) {
		parent := filepath.Dir(initial)
		if parent == initial || parent == "." {
			initial = ""
			break
		}
		initial = parent
	}
	if initial != "" {
		var folder *shellItem
		path, pathErr := windows.UTF16PtrFromString(initial)
		if pathErr != nil {
			return nil, pathErr
		}
		hr, _, _ := shell32.NewProc("SHCreateItemFromParsingName").Call(uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&shellItemIID)), uintptr(unsafe.Pointer(&folder)))
		if err = pathDialogHRESULT("SHCreateItemFromParsingName", hr); err != nil {
			return nil, err
		}
		defer syscall.SyscallN(folder.vtbl.Release, uintptr(unsafe.Pointer(folder)))
		if err = dialog.call(dialog.vtbl.SetFolder, uintptr(unsafe.Pointer(folder))); err != nil {
			return nil, err
		}
	}
	return dialog, nil
}

func pathDialogDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Arguments may contain pointers to out-parameters; keep their storage stable
// across this Go wrapper as well as the actual syscall.
//
//go:uintptrescapes
func (d *fileDialog) call(method uintptr, args ...uintptr) error {
	hr, _, _ := syscall.SyscallN(method, append([]uintptr{uintptr(unsafe.Pointer(d))}, args...)...)
	return pathDialogHRESULT("IFileDialog", hr)
}

func (d *fileDialog) setText(method uintptr, value string) error {
	text, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	err = d.call(method, uintptr(unsafe.Pointer(text)))
	runtime.KeepAlive(text)
	return err
}

func pathDialogHRESULT(operation string, hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s HRESULT 0x%08x", operation, uint32(hr))
	}
	return nil
}

func newPathDialogOwner() (_ uintptr, _ func(), err error) {
	// Capture the browser before creating any helper windows. A local proxy
	// keeps modality and forced cancellation from disabling the foreign window.
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	if valid, _, _ := user32.NewProc("IsWindow").Call(foreground); valid == 0 {
		foreground = 0
	}
	icon, err := makeIcon(false)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if err != nil {
			user32.NewProc("DestroyIcon").Call(icon)
		}
	}()
	instance, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	className := wide("SWD.PathDialog.Owner")
	wc := windowClass{Proc: user32.NewProc("DefWindowProcW").Addr(), Instance: instance, Icon: icon, Name: className}
	if ok, _, cause := user32.NewProc("RegisterClassW").Call(uintptr(unsafe.Pointer(&wc))); ok == 0 {
		return 0, nil, fmt.Errorf("注册选择器窗口：%w", cause)
	}
	defer func() {
		if err != nil {
			user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(className)), instance)
		}
	}()
	var info struct {
		Size         uint32
		Bounds, Work struct{ Left, Top, Right, Bottom int32 }
		Flags        uint32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	monitor, _, _ := user32.NewProc("MonitorFromWindow").Call(foreground, 2) // MONITOR_DEFAULTTONEAREST
	if ok, _, cause := user32.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0, nil, fmt.Errorf("读取选择器显示器：%w", cause)
	}
	x, y := (info.Work.Left+info.Work.Right)/2, (info.Work.Top+info.Work.Bottom)/2
	// Use an overlapped window (as WinForms did), not WS_POPUP: Shell walks
	// popup owners and would inherit the browser's icon instead of this one.
	// LAYERED makes the 1px owner transparent; ownership hides its taskbar entry.
	hwnd, _, cause := user32.NewProc("CreateWindowExW").Call(0x80000,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(wide("Still Wanna Dance"))), 0,
		uintptr(x), uintptr(y), 1, 1, foreground, 0, instance, 0)
	if hwnd == 0 {
		return 0, nil, fmt.Errorf("创建选择器窗口：%w", cause)
	}
	defer func() {
		if err != nil {
			user32.NewProc("DestroyWindow").Call(hwnd)
		}
	}()
	cleanup := func() {
		user32.NewProc("DestroyWindow").Call(hwnd)
		user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(className)), instance)
		user32.NewProc("DestroyIcon").Call(icon)
	}
	if ok, _, cause := user32.NewProc("SetLayeredWindowAttributes").Call(hwnd, 0, 0, 2); ok == 0 { // LWA_ALPHA
		return 0, nil, fmt.Errorf("隐藏选择器所属窗口：%w", cause)
	}
	user32.NewProc("SendMessageW").Call(hwnd, 0x80, 0, icon) // WM_SETICON / ICON_SMALL
	user32.NewProc("SendMessageW").Call(hwnd, 0x80, 1, icon) // ICON_BIG
	// Consume the hidden process's STARTUPINFO/SW_HIDE on this owner rather
	// than the actual dialog; do not activate or flash the proxy window.
	user32.NewProc("ShowWindow").Call(hwnd, 4) // SW_SHOWNOACTIVATE
	user32.NewProc("ShowWindow").Call(hwnd, 0) // SW_HIDE
	return hwnd, cleanup, nil
}
