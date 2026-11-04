package apppaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkOwnershipFollowsExecutableAndSurvivesPortableMove(t *testing.T) {
	root := t.TempDir()
	first, moved := filepath.Join(root, "original"), filepath.Join(root, "moved")
	exe := filepath.Join(first, "SWD.exe")
	config := DefaultConfig(exe)
	work := WorkForConfig(exe, config)
	if filepath.Dir(work) != filepath.Join(first, "work") {
		t.Fatal("work is not beside executable", work)
	}
	if filepath.Base(work) != filepath.Base(WorkForConfig(filepath.Join(moved, "SWD.exe"), filepath.Join(moved, "config.json"))) {
		t.Fatal("portable move changed the work owner")
	}
	external := filepath.Join(root, "external", "config.json")
	if externalWork := WorkForConfig(exe, external); filepath.Dir(externalWork) != filepath.Dir(work) || externalWork == work {
		t.Fatal("external config moved or shared the workspace", externalWork)
	}
	if WorkForConfig(exe, filepath.Join(first, "data", "other.json")) == work {
		t.Fatal("different configs share block files")
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("path calculation created files", err)
	}
	if runtime.GOOS == "windows" && work != WorkForConfig(exe, filepath.Join(first, "CONFIG.JSON")) {
		t.Fatal("Windows case alias got a different owner")
	}
}

func TestDataLocationsCannotOverlapDisposableWork(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "SWD.exe")
	for _, path := range []string{root, filepath.Join(root, "work"), filepath.Join(root, "work", "old-instance", "storage.sqlite")} {
		if err := ValidateWorkSeparation(exe, path); err == nil {
			t.Fatal("accepted overlap", path)
		}
	}
	for _, path := range []string{DefaultConfig(exe), filepath.Join(root, "data"), filepath.Join(root, "data", "storage"), filepath.Join(root, "data", "work-other"), filepath.Join(t.TempDir(), "external.json")} {
		if err := ValidateWorkSeparation(exe, path); err != nil {
			t.Fatal("rejected independent data", path, err)
		}
	}
}

func TestCustomWorkRootSeparatesInstallations(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "ssd")
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	first := WorkForCacheRoot(filepath.Join(a, "SWD.exe"), filepath.Join(a, "config.json"), cache)
	second := WorkForCacheRoot(filepath.Join(b, "SWD.exe"), filepath.Join(b, "config.json"), cache)
	if first == second || filepath.Dir(first) != cache || filepath.Dir(second) != cache {
		t.Fatal("selected root did not isolate the installations", first, second)
	}
	if !strings.HasPrefix(filepath.Base(first), "console-") || len(filepath.Base(first)) != len("console-")+32 {
		t.Fatal("selected root did not contain the expected instance directory", first)
	}
	if actual := WorkForCacheRoot(filepath.Join(a, "SWD.exe"), filepath.Join(a, "config.json"), ""); actual != WorkForConfig(filepath.Join(a, "SWD.exe"), filepath.Join(a, "config.json")) {
		t.Fatal("empty selection changed the default workspace", actual)
	}
}
