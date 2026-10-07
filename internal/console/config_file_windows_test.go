package console

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigurationReplacementFailurePreservesSavedFile(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	// Allow reads (including the version check), but deny replacement of the
	// destination. This exercises failure after the temporary file is synced.
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != windows.InvalidHandle {
			windows.CloseHandle(h)
		}
	}()
	want := c.savedSettings
	next := want
	next.QueuePrefetchCount++
	if err := c.save(next); err == nil {
		t.Fatal("replaced a locked configuration")
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || !bytes.Equal(before, after) || c.savedSettings != want {
		t.Fatal("failed replacement changed persisted settings", err)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(c.configPath), ".settings-*"))
	if err != nil || len(temps) != 0 {
		t.Fatal("failed save left temporary files", temps, err)
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = windows.InvalidHandle
	if err := c.save(next); err != nil {
		t.Fatal("retry failed", err)
	}
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.settings != next {
		t.Fatal("retry did not persist new settings")
	}
}
