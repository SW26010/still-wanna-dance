package desktop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wtsAPI                = windows.NewLazySystemDLL("wtsapi32.dll")
	wtsEnumerateProcesses = wtsAPI.NewProc("WTSEnumerateProcessesExW")
	wtsFreeProcessMemory  = wtsAPI.NewProc("WTSFreeMemoryExW")
)

// WTS_PROCESS_INFOW, returned at information level 0.
type wtsProcessInfo struct {
	sessionID uint32
	processID uint32
	name      *uint16
	userSID   *windows.SID
}

func findVRChatProcess() (processWaiter, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return nil, err
	}
	return findProcessInSession("VRChat.exe", session)
}

func processIDsInSession(name string, session uint32) ([]uint32, error) {
	// WTS supplies names and session IDs together without querying each game
	// process. In particular, no game PROCESS_QUERY_INFORMATION access is needed.
	var entries *wtsProcessInfo
	var count, level uint32
	ok, _, err := wtsEnumerateProcesses.Call(0, uintptr(unsafe.Pointer(&level)), uintptr(session), uintptr(unsafe.Pointer(&entries)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 {
		return nil, err
	}
	defer wtsFreeProcessMemory.Call(0, uintptr(unsafe.Pointer(entries)), uintptr(count)) // WTSTypeProcessInfoLevel0
	var pids []uint32
	for _, entry := range unsafe.Slice(entries, count) {
		if entry.sessionID == session && strings.EqualFold(windows.UTF16PtrToString(entry.name), name) {
			pids = append(pids, entry.processID)
		}
	}
	return pids, nil
}

func findProcessInSession(name string, session uint32) (processWaiter, error) {
	pids, err := processIDsInSession(name, session)
	if err != nil {
		return nil, err
	}
	for _, pid := range pids {
		// Waiting needs no game memory access or query-information permission.
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue // exited between enumeration and OpenProcess
		}
		if err != nil {
			return nil, err
		}
		// Revalidate after opening: a recycled PID must not attach the wait to
		// an unrelated process. The retained handle then identifies that object.
		current, err := processIDsInSession(name, session)
		if err == nil && slices.Contains(current, pid) {
			return processHandle(handle), nil
		}
		_ = windows.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type processHandle windows.Handle

func (p processHandle) Close() error { return windows.CloseHandle(windows.Handle(p)) }

func (p processHandle) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canceled, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(canceled)
	notified := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(notified)
		_ = windows.SetEvent(canceled)
	})
	defer func() {
		if !stop() {
			<-notified // never close an event while the callback may still use it
		}
	}()
	result, err := windows.WaitForMultipleObjects([]windows.Handle{canceled, windows.Handle(p)}, false, windows.INFINITE)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0+1 {
		return fmt.Errorf("unexpected process wait result: %d", result)
	}
	return nil
}
