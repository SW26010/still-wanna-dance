package desktop

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var taskDialog = windows.NewLazySystemDLL("comctl32.dll").NewProc("TaskDialogIndirect")

// CommCtrl.h packs TASKDIALOGCONFIG to one byte. Byte-array pointers avoid Go's
// 8-byte pointer alignment on 64-bit Windows; all remaining fields are DWORDs.
type dialogPointer [unsafe.Sizeof(uintptr(0))]byte

func packedDialogPointer(value uintptr) (p dialogPointer) {
	if len(p) == 8 {
		binary.LittleEndian.PutUint64(p[:], uint64(value))
	} else {
		binary.LittleEndian.PutUint32(p[:], uint32(value))
	}
	return p
}

type taskDialogConfig struct {
	Size                 uint32
	Parent, Instance     dialogPointer
	Flags, CommonButtons uint32
	Title, Icon          dialogPointer
	Instruction, Content dialogPointer
	ButtonCount          uint32
	Buttons              dialogPointer
	DefaultButton        int32
	RadioButtonCount     uint32
	RadioButtons         dialogPointer
	DefaultRadioButton   int32
	VerificationText     dialogPointer
	ExpandedInformation  dialogPointer
	ExpandedControlText  dialogPointer
	CollapsedControlText dialogPointer
	FooterIcon, Footer   dialogPointer
	Callback, Data       dialogPointer
	Width                uint32
}

func ShowError(cause error) {
	if err := showTaskDialog(cause); err != nil {
		// Keep startup failures visible even if Windows cannot create its task dialog.
		fmt.Fprintf(os.Stderr, "无法显示 TaskDialog：%v\n", err)
		user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(wide(cause.Error()))), uintptr(unsafe.Pointer(wide("Still Wanna Dance"))), 0x10)
	}
}

func showTaskDialog(cause error) error {
	// Task dialogs require STA; keep initialization, modal UI and cleanup on
	// the same OS thread, including calls made outside the tray message loop.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restoreDPI, err := enterPerMonitorDPI()
	if err != nil {
		return err
	}
	defer restoreDPI()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
		return err
	}
	defer windows.CoUninitialize()
	if err := taskDialog.Find(); err != nil {
		return err
	}
	instruction, content := "无法完成操作", cause.Error()
	var width uint32
	var conflict *InstanceConflictError
	if errors.As(cause, &conflict) {
		instruction = instanceConflictInstruction
		content = instanceConflictExplanation + "\n\n" + conflict.details()
		width = 220 // Dialog units: Windows scales the layout for its font and DPI.
	}
	// TD_ERROR_ICON and TDCBF_CLOSE_BUTTON. The application's embedded manifest
	// selects Common Controls v6, including for ordinary offline Go builds.
	titleText, instructionText, contentText := wide("Still Wanna Dance"), wide(instruction), wide(content)
	// The packed ABI fields are not Go pointers. Pin the strings until the modal
	// call returns so native code can safely retain them throughout the dialog.
	var pinned runtime.Pinner
	pinned.Pin(titleText)
	pinned.Pin(instructionText)
	pinned.Pin(contentText)
	defer pinned.Unpin()
	config := taskDialogConfig{
		Size: uint32(unsafe.Sizeof(taskDialogConfig{})), CommonButtons: 0x20,
		Title: packedDialogPointer(uintptr(unsafe.Pointer(titleText))), Icon: packedDialogPointer(0xfffe),
		Instruction: packedDialogPointer(uintptr(unsafe.Pointer(instructionText))),
		Content:     packedDialogPointer(uintptr(unsafe.Pointer(contentText))), Width: width,
	}
	result, _, _ := taskDialog.Call(uintptr(unsafe.Pointer(&config)), 0, 0, 0)
	if int32(result) < 0 {
		return fmt.Errorf("TaskDialogIndirect HRESULT 0x%08x", uint32(result))
	}
	return nil
}
