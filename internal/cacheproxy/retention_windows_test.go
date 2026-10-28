package cacheproxy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRetentionWaitsForWindowsPendingDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.mp4")
	putRetained(t, path, 1)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := os.NewFile(uintptr(handle), path)
	t.Cleanup(func() { held.Close() })
	pending := byte(1)
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &pending, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsPermission(err) {
		t.Fatalf("expected access denied while deletion is pending: %v", err)
	}
	// Keep the real Windows pending-delete state long enough for the assertion to observe it.
	closed := make(chan error, 1)
	time.AfterFunc(100*time.Millisecond, func() { closed <- held.Close() })
	t.Cleanup(func() {
		if err := <-closed; err != nil {
			t.Error(err)
		}
	})
	expectRetained(t, path, false)
}
