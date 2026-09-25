package desktop

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

type mutexLease struct {
	once    sync.Once
	release chan struct{}
	done    chan error
	err     error
}

func (l *mutexLease) Close() error {
	l.once.Do(func() { close(l.release); l.err = <-l.done })
	return l.err
}

// Mutex ownership belongs to an OS thread, not a goroutine. A dedicated locked
// thread holds the lease and releases it even when Close runs on another thread.
func acquire(name string) (Lease, error) {
	type result struct {
		lease Lease
		err   error
	}
	resultCh := make(chan result)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		p, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		h, _, callErr := kernel32.NewProc("CreateMutexW").Call(0, 1, uintptr(unsafe.Pointer(p)))
		if h == 0 {
			resultCh <- result{err: fmt.Errorf("CreateMutexW: %w", callErr)}
			return
		}
		if callErr == syscall.Errno(183) {
			status, _, waitErr := kernel32.NewProc("WaitForSingleObject").Call(h, 0)
			if status != 0 && status != 0x80 {
				_ = syscall.CloseHandle(syscall.Handle(h))
				if status == 0x102 {
					resultCh <- result{}
					return
				}
				resultCh <- result{err: fmt.Errorf("等待实例互斥锁失败 (%x): %v", status, waitErr)}
				return
			}
		}
		lease := &mutexLease{release: make(chan struct{}), done: make(chan error, 1)}
		resultCh <- result{lease: lease}
		<-lease.release
		r, _, releaseErr := kernel32.NewProc("ReleaseMutex").Call(h)
		err = syscall.CloseHandle(syscall.Handle(h))
		if r == 0 {
			err = fmt.Errorf("ReleaseMutex: %w", releaseErr)
		}
		lease.done <- err
	}()
	r := <-resultCh
	return r.lease, r.err
}
