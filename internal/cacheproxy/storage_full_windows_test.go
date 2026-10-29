package cacheproxy

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Opt in only through scripts/test-storage-full.ps1. Both the harness and this
// test bound the volume size; never consume free space on an ordinary drive.
func TestBoundedVolumeExhaustion(t *testing.T) {
	root := os.Getenv("SWD_FULL_TEST_VOLUME")
	if root == "" {
		t.Skip("run scripts/test-storage-full.ps1 with a disposable 128 MiB VHD")
	}
	if filepath.Clean(root) != filepath.VolumeName(root)+`\` {
		t.Fatal("expected a dedicated drive root", root)
	}
	marker, err := os.ReadFile(filepath.Join(root, ".swd-disk-full-test"))
	if err != nil || string(marker) != "bounded-ntfs-v1\n" {
		t.Fatal("missing disposable-volume marker", err)
	}
	_, total := boundedVolumeSpace(t, root)
	t.Logf("bounded volume: %s, total bytes: %d", root, total)
	for _, target := range []string{"blocks", "publication"} {
		t.Run(target, func(t *testing.T) {
			dir := boundedVolumeDirectory(t, root)
			body := strings.Repeat("video content ", 1<<17)
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }, func(cfg *Config) {
				cfg.CacheUnlimited = true
				if target == "publication" {
					cfg.VideoDir = dir
				} else {
					work, err := OpenWorkDirectory(dir, cfg.Logger)
					if err != nil {
						t.Fatal(err)
					}
					cfg.Work = work
					t.Cleanup(func() { work.Close() })
				}
			})
			if err := cfg.Store.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
				t.Fatal(err)
			}
			// A stale owned block must be reclaimable even while the volume is
			// full. The unknown neighbor is deliberately outside the whitelist.
			stale := filepath.Join(dir, "download-block-123456.part")
			unknown := filepath.Join(dir, "keep.txt")
			if target == "blocks" {
				if err := os.WriteFile(stale, make([]byte, 1<<20), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			releaseSpace := exhaustBoundedVolume(t, root, dir)
			_, err := s.Prefetch(t.Context(), videoURL(body))
			if !errors.Is(err, ErrLocalStorage) || !isVolumeFull(err) || s.StorageError() == nil {
				t.Fatal("real disk exhaustion was not reported as a storage failure", err, s.StorageError())
			}
			if _, err := os.Stat(cfg.videoFile(videoKey(body))); !os.IsNotExist(err) {
				t.Fatal("failed download published a complete video", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal("engine could not stop on a full volume", err)
			}
			policies, err := cfg.Store.SongPolicies(t.Context())
			if err != nil || policies[42] != SongFavorite {
				t.Fatal("media failure damaged independently stored personal data", policies, err)
			}
			if target == "blocks" {
				if err := cfg.Work.Close(); err != nil {
					t.Fatal(err)
				}
				before, _ := boundedVolumeSpace(t, root)
				work, err := OpenWorkDirectory(dir, nil)
				if err != nil {
					t.Fatal("next startup could not reclaim owned blocks on the full volume", err)
				}
				if err := work.Close(); err != nil {
					t.Fatal(err)
				}
				after, _ := boundedVolumeSpace(t, root)
				if _, err := os.Stat(stale); !os.IsNotExist(err) || after <= before {
					t.Fatal("startup did not reclaim the stale block", before, after, err)
				}
				if got, err := os.ReadFile(unknown); err != nil || string(got) != "keep" {
					t.Fatal("startup removed unknown data", err)
				}
			} else {
				entries, err := os.ReadDir(cfg.publishDir())
				if err != nil || len(entries) != 0 {
					t.Fatal("failed publication left staging files", entries, err)
				}
			}
			releaseSpace()
			t.Log("real full-volume failure was visible; stop, core-data reads and cleanup succeeded")
		})
	}
	t.Run("database", func(t *testing.T) {
		dir := boundedVolumeDirectory(t, root)
		path := filepath.Join(dir, "personal.sqlite")
		golden, err := os.ReadFile("testdata/storage-v8.sqlite")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, golden, 0600); err != nil {
			t.Fatal(err)
		}
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })

		if err := store.usage.start(0, true); err != nil {
			t.Fatal(err)
		}
		store.usage.flush()
		before := snapshotBusinessTables(t, store.db)
		releaseSpace := exhaustBoundedVolume(t, root, dir)
		err = store.SetSongPolicy(t.Context(), 42, SongBlocked)
		if !isVolumeFull(err) || !errors.Is(store.Error(), ErrLocalStorage) {
			t.Fatal("synchronous database failure was not visible without an engine", err, store.Error())
		}
		store.usage.startGET("42", strings.Repeat("a", 32))
		store.usage.flush()
		if err := store.Close(); !isVolumeFull(err) {
			t.Fatal("close did not report failed accepted background work", err)
		}
		releaseSpace()
		restored, err := OpenStore(path)
		if err != nil {
			t.Fatal("database could not reopen after space was freed", err)
		}
		defer restored.Close()
		if got := snapshotBusinessTables(t, restored.db); !reflect.DeepEqual(got, before) {
			t.Fatal("failed writes changed committed business data")
		}
		var integrity string
		if err := restored.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatal("database integrity after full-volume failure", integrity, err)
		}
		if err := restored.SetSongPolicy(t.Context(), 42, SongBlocked); err != nil {
			t.Fatal("new application instance could not resume writes", err)
		}
		t.Log("synchronous and background failures were visible; all 14 tables survived and writes resumed after reopening")
	})
}

func boundedVolumeSpace(t *testing.T, root string) (free, total uint64) {
	t.Helper()
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.GetDiskFreeSpaceEx(path, &free, &total, nil); err != nil {
		t.Fatal(err)
	}
	if total < 32<<20 || total > 128<<20 {
		t.Fatalf("refusing to fill a volume outside the 32–128 MiB bound: %d bytes", total)
	}
	return free, total
}

func boundedVolumeDirectory(t *testing.T, root string) string {
	t.Helper()
	dir, err := os.MkdirTemp(root, "swd-full-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func isVolumeFull(err error) bool {
	if errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) {
		return true
	}
	var sqlErr *sqlite.Error
	return errors.As(err, &sqlErr) && sqlErr.Code()&0xff == sqlite3.SQLITE_FULL
}

func exhaustBoundedVolume(t *testing.T, root, dir string) func() {
	t.Helper()
	boundedVolumeSpace(t, root)
	f, err := os.CreateTemp(dir, "bounded-fill-")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			f.Close()
			if err := os.Remove(f.Name()); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	var written int64
	// Finish with cluster-sized writes after a large write no longer fits.
	for _, size := range []int{1 << 20, 4096} {
		data := make([]byte, size)
		for {
			n, err := f.Write(data)
			written += int64(n)
			if written > 128<<20 {
				t.Fatal("bounded-volume write limit exceeded")
			}
			if err != nil {
				if !isVolumeFull(err) {
					t.Fatal("fixture did not reach a real disk-full error", err)
				}
				break
			}
		}
	}
	if err := f.Sync(); err != nil && !isVolumeFull(err) {
		t.Fatal(err)
	}
	free, _ := boundedVolumeSpace(t, root)
	t.Logf("real disk-full error after %d filler bytes; available bytes: %d", written, free)
	return release
}
