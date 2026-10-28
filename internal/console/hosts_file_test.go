package console

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHostsWriteRejectsChangedFile(t *testing.T) {
	path := hostsFixture(t, hostsBaseline+"# later user change\r\n")
	before := readHostsFixture(t, path)
	next, err := transformHosts(hostsBaseline, "enable")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHostsChange(path, []byte(hostsBaseline), []byte(next)); err == nil {
		t.Fatal("overwrote hosts changed since the operation read it")
	}
	if readHostsFixture(t, path) != before {
		t.Fatal("failed write changed hosts")
	}
	if _, err := os.Stat(path + ".swd-backup"); !os.IsNotExist(err) {
		t.Fatal("stale input created a misleading backup", err)
	}
}

type hostsReadFunc func([]byte) (int, error)

func (f hostsReadFunc) Read(p []byte) (int, error) { return f(p) }

func assertNoHostsStages(t *testing.T, path string) {
	t.Helper()
	stages, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".hosts.swd-*"))
	if err != nil || len(stages) != 0 {
		t.Fatal("hosts staging files were not cleaned", stages, err)
	}
}

func TestHostsPartialStageNeverChangesOriginalOrOldBackup(t *testing.T) {
	path := hostsFixture(t, hostsBaseline+"# later user mapping\r\n")
	before := readHostsFixture(t, path)
	if err := os.WriteFile(path+".swd-backup", []byte(hostsBaseline), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected interrupted write")
	contents := io.MultiReader(strings.NewReader("partial replacement"), hostsReadFunc(func([]byte) (int, error) { return 0, failure }))
	if err := replaceHostsFile(path, []byte(before), contents); !errors.Is(err, failure) {
		t.Fatal("missing interrupted-stage error", err)
	}
	if readHostsFixture(t, path) != before || readHostsFixture(t, path+".swd-backup") != hostsBaseline {
		t.Fatal("partial replacement changed hosts or its old backup")
	}
	assertNoHostsStages(t, path)
	// Retry still uses the intact current file, including later user changes.
	if err := applyHosts(path, "enable"); err != nil {
		t.Fatal(err)
	}
	if err := applyHosts(path, "disable"); err != nil {
		t.Fatal(err)
	}
	if readHostsFixture(t, path) != before {
		t.Fatal("retry restored the stale backup instead of current user mappings")
	}
	assertNoHostsStages(t, path)
}

func TestHostsReplacementRejectsConcurrentEdit(t *testing.T) {
	path := hostsFixture(t, hostsBaseline)
	edited := hostsBaseline + "# changed while preparing\r\n"
	contents := hostsReadFunc(func(p []byte) (int, error) {
		if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
			t.Fatal(err)
		}
		return copy(p, "replacement"), io.EOF
	})
	if err := replaceHostsFile(path, []byte(hostsBaseline), contents); err == nil {
		t.Fatal("replaced a concurrent edit")
	}
	if readHostsFixture(t, path) != edited {
		t.Fatal("concurrent user edit was lost")
	}
	assertNoHostsStages(t, path)
}

func TestHostsReplacementKeepsUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows checks DACLs separately")
	}
	path := hostsFixture(t, hostsBaseline)
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if err := replaceHostsFile(path, []byte(hostsBaseline), bytes.NewReader([]byte("replacement"))); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("hosts permissions changed", info, err)
	}
}
