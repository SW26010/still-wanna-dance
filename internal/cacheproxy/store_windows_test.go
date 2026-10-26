package cacheproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStoreRejectsShortFilenameAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal-library.sqlite")
	s, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n >= uint32(len(buf)) {
		t.Fatal("cannot inspect short name", n, err)
	}
	alias := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(path, alias) {
		t.Skip("filesystem did not assign an 8.3 alias")
	}
	t.Logf("short filename: %s", filepath.Base(alias))
	if opened, err := OpenStore(alias); err == nil {
		opened.Close()
		t.Fatal("accepted a noncanonical filename with a different companion namespace")
	}
	if db, err := readDatabaseFile(alias); err == nil {
		db.Close()
		t.Fatal("read through a short filename alias")
	}
	if !strings.EqualFold(filepath.Dir(alias), filepath.Dir(path)) {
		if created, err := CreateStore(filepath.Join(filepath.Dir(alias), "new.sqlite")); err == nil {
			created.Close()
			t.Fatal("created a database under a short directory alias")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), "new.sqlite")); !os.IsNotExist(err) {
			t.Fatal("rejected path left an empty database", err)
		}
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal("normal database did not reopen", err)
	}
	defer s.Close()
	if other, err := OpenStore(strings.ToUpper(path)); err == nil {
		other.Close()
		t.Fatal("case-only path bypassed the existing lease")
	}
}
