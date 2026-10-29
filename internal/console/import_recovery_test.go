package console

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/workfiles"
)

func prepareImportRecovery(t *testing.T, names ...string) (*Console, videoImportTask) {
	t.Helper()
	c := testConsole(t)
	c.settings.VideoStorageMode = "unlimited"
	if err := saveConfigSettings(c.configPath, c.settings); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var songs []cacheproxy.CatalogSong
	for i, name := range names {
		body := []byte("known video " + name)
		if err := os.WriteFile(filepath.Join(root, name+".mp4"), body, 0600); err != nil {
			t.Fatal(err)
		}
		songs = append(songs, cacheproxy.CatalogSong{ID: int64(i + 1), MD5: fmt.Sprintf("%x", md5.Sum(body))})
	}
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261030000000", Source: cacheproxy.KivaCatalogURL, Songs: songs}); err != nil {
		t.Fatal(err)
	}
	files, err := enumerateImport(t.Context(), root, c.settings.VideoDir)
	if err != nil {
		t.Fatal(err)
	}
	task := videoImportTask{Version: 1, ID: 42, Source: root, DatabasePath: c.settings.DatabasePath, VideoDir: c.settings.VideoDir, Enumerated: true, Files: files}
	if err := c.commitImportTask(task); err != nil {
		t.Fatal(err)
	}
	return c, task
}

func waitImport(t *testing.T, c *Console) ImportStatus {
	t.Helper()
	c.mu.Lock()
	done := c.importDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("no import was started")
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("import did not stop")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.importStatus
}

func TestImportRecoveryUsesFrozenManifestAndRestoresReceipts(t *testing.T) {
	c, task := prepareImportRecovery(t, "a-good", "b-moved", "c-deleted", "d-modified", "e-replaced")
	if err := os.Rename(task.Files[1].File.Source, task.Files[1].File.Source+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(task.Files[2].File.Source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task.Files[3].File.Source, []byte("different content"), 0600); err != nil {
		t.Fatal(err)
	}
	replaced := task.Files[4].File
	body, err := os.ReadFile(replaced.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replaced.Source, replaced.Source+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replaced.Source, body, 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(0, replaced.Modified)
	if err := os.Chtimes(replaced.Source, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.Source, "new.mp4"), []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if !c.importStatus.Paused || c.importStatus.Running || c.service != nil || c.importStatus.Total != 5 {
		t.Fatal("restart resumed or rescanned", c.importStatus)
	}
	if err := c.startImport(task.Source); err == nil {
		t.Fatal("new task erased recoverable receipts")
	}
	if err := c.resumeImport(task.ID); err != nil {
		t.Fatal(err)
	}
	s := waitImport(t, c)
	if s.Imported != 1 || s.Skipped != 4 || s.Total != 5 || s.Paused || s.Failed != 0 {
		t.Fatal(s)
	}
	for _, item := range c.importTask.Files[1:] {
		if item.Reason != cacheproxy.ErrImportSourceChanged.Error() {
			t.Fatal("changed source not skipped", item)
		}
	}
	c = restartTestConsole(t, c)
	if c.importStatus.Running || !c.importStatus.CleanupAvailable || c.service != nil {
		t.Fatal("receipt recovery started work", c.importStatus)
	}
	if err := c.cleanupImport(task.ID, false); err == nil {
		t.Fatal("cleanup bypassed confirmation")
	}
	if err := c.cleanupImport(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); s.Deleted != 1 || s.CleanupAvailable || s.Error != "" {
		t.Fatal(s)
	}
	if _, err := os.Stat(task.Files[0].File.Source); !os.IsNotExist(err) {
		t.Fatal("confirmed source not removed", err)
	}
	if _, err := os.Stat(replaced.Source); err != nil {
		t.Fatal("replacement was removed", err)
	}
}

func TestImportRecoverySaveFailureStopsBeforeNextFile(t *testing.T) {
	c, task := prepareImportRecovery(t, "a", "b")
	c.lifecycleMu.Lock()
	engine, err := c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	path := c.files().State("video-import")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(filepath.Dir(path), workfiles.TempName(path))
	if err := os.WriteFile(slot, []byte("occupied work slot"), 0600); err != nil {
		t.Fatal(err)
	}
	err = c.runImport(t.Context(), engine, task)
	if err == nil || !strings.Contains(err.Error(), "保存导入进度失败") {
		t.Fatal(err)
	}
	if c.importTask.Files[0].Receipt != nil || c.importStatus.Checked != 0 {
		t.Fatal("uncommitted receipt exposed", c.importStatus)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("last durable record lost", err)
	}
	key := func(name string) string { return fmt.Sprintf("%x", md5.Sum([]byte("known video "+name))) }
	if _, err := os.Stat(filepath.Join(task.VideoDir, key("a")+".mp4")); err != nil {
		t.Fatal("publication did not precede failed save", err)
	}
	if _, err := os.Stat(filepath.Join(task.VideoDir, key("b")+".mp4")); !os.IsNotExist(err) {
		t.Fatal("processed next file after save failure", err)
	}
	if err := os.Remove(slot); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if err := c.resumeImport(task.ID); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); s.Imported != 1 || s.Skipped != 1 || s.Failed != 0 {
		t.Fatal(s)
	}
	if c.importTask.Files[0].Receipt != nil {
		t.Fatal("unrecorded publication gained cleanup eligibility")
	}
	if err := c.cleanupImport(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); s.Deleted != 1 {
		t.Fatal(s)
	}
	if _, err := os.Stat(task.Files[0].File.Source); err != nil {
		t.Fatal("unreceipted source removed", err)
	}
}

func TestImportRecoveryScopeAndUnreadableRecord(t *testing.T) {
	c, task := prepareImportRecovery(t, "a")
	settings := c.settings
	settings.VideoDir = filepath.Join(t.TempDir(), "other-videos")
	if err := saveConfigSettings(c.configPath, settings); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if !c.importStatus.ScopeMismatch || c.service != nil {
		t.Fatal(c.importStatus)
	}
	if err := c.resumeImport(task.ID); err == nil {
		t.Fatal("resumed in another library")
	}
	if err := c.cleanupImport(task.ID, true); err == nil {
		t.Fatal("cleaned in another library")
	}
	if err := c.discardImport(task.ID+1, true); err == nil {
		t.Fatal("stale discard accepted")
	}
	if err := c.discardImport(task.ID, false); err == nil {
		t.Fatal("unconfirmed discard accepted")
	}
	if err := c.discardImport(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(task.Files[0].File.Source); err != nil {
		t.Fatal("discard removed source", err)
	}
	path := c.files().State("video-import")
	if err := os.WriteFile(path, []byte("broken record"), 0600); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if !c.importStatus.Present || c.importStatus.Error == "" || c.importStatus.CleanupAvailable || c.service != nil {
		t.Fatal(c.importStatus)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "broken record" {
		t.Fatal("bad record overwritten", err)
	}
	if err := c.discardImport(c.importStatus.ID, true); err != nil {
		t.Fatal(err)
	}
}

func TestImportPauseAndExitKeepCurrentFilePending(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			c, task := prepareImportRecovery(t, "large")
			f, err := os.Create(task.Files[0].File.Source)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(128 << 20); err != nil {
				t.Fatal(err)
			}
			f.Close()
			task.Files[0].File, err = cacheproxy.SnapshotImportFile(task.Files[0].File.Source)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.commitImportTask(task); err != nil {
				t.Fatal(err)
			}
			if err := c.resumeImport(task.ID); err != nil {
				t.Fatal(err)
			}
			if shutdown {
				c.BeginShutdown()
			} else if err := c.pauseImport(task.ID); err != nil {
				t.Fatal(err)
			}
			if s := waitImport(t, c); s.Running || !s.Paused || s.Checked != 0 || s.Failed != 0 || s.Error != "" {
				t.Fatal("pause consumed unfinished file", s)
			}
			c = restartTestConsole(t, c)
			if !c.importStatus.Paused || c.service != nil || c.importTask.Files[0].Result != "" {
				t.Fatal("restart resumed work", c.importStatus)
			}
		})
	}
}

// A test-only context stops at the existing staging seam. No product fault hook.
type importStageCrashContext struct {
	context.Context
	videoDir, marker string
}

func (c importStageCrashContext) Err() error {
	stages, _ := filepath.Glob(filepath.Join(c.videoDir, ".swd-tmp", "import-*", "video"))
	for _, stage := range stages {
		if info, err := os.Stat(stage); err == nil && info.Size() > 0 {
			_ = os.WriteFile(c.marker, []byte(strconv.FormatInt(info.Size(), 10)), 0600)
			<-c.Done()
		}
	}
	return c.Context.Err()
}

func TestImportCrashWorker(t *testing.T) {
	config := os.Getenv("SWD_IMPORT_CRASH_CONFIG")
	if config == "" {
		t.Skip("subprocess helper")
	}
	c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	c.lifecycleMu.Lock()
	engine, err := c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	task, mode, marker := c.importTask.clone(), os.Getenv("SWD_IMPORT_CRASH_MODE"), os.Getenv("SWD_IMPORT_CRASH_MARKER")
	if mode == "stage" || mode == "copy" {
		err = c.runImport(importStageCrashContext{context.Background(), task.VideoDir, marker}, engine, task)
	} else if mode == "published" {
		allowed, e := engine.KnownMedia(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		_, err = engine.ImportSnapshotVideo(t.Context(), task.Files[0].File, allowed)
	} else {
		err = engine.RemoveImportedSource(t.Context(), *task.Files[0].Receipt)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	<-context.Background().Done()
}

func TestImportCrashRecovery(t *testing.T) {
	for _, mode := range []string{"stage", "published", "cleanup", "copy"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "copy" && os.Getenv("SWD_IMPORT_TEST_VOLUME") == "" {
				t.Skip("set SWD_IMPORT_TEST_VOLUME for cross-volume copy interruption")
			}
			c, task := prepareImportRecovery(t, "a", "b")
			if mode == "copy" {
				source, err := os.MkdirTemp(os.Getenv("SWD_IMPORT_TEST_VOLUME"), "swd-import-crash-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(source); err != nil {
						t.Error(err)
					}
				})
				var songs []cacheproxy.CatalogSong
				for i, item := range task.Files {
					body, err := os.ReadFile(item.File.Source)
					if err != nil {
						t.Fatal(err)
					}
					body = bytes.Repeat(body, 65536)
					if err := os.WriteFile(filepath.Join(source, filepath.Base(item.File.Source)), body, 0600); err != nil {
						t.Fatal(err)
					}
					songs = append(songs, cacheproxy.CatalogSong{ID: int64(i + 1), MD5: fmt.Sprintf("%x", md5.Sum(body))})
				}
				if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261030000001", Source: cacheproxy.KivaCatalogURL, Songs: songs}); err != nil {
					t.Fatal(err)
				}
				task.Source = source
				task.Files, err = enumerateImport(t.Context(), source, task.VideoDir)
				if err != nil {
					t.Fatal(err)
				}
				if err := c.commitImportTask(task); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cleanup" {
				if err := c.resumeImport(task.ID); err != nil {
					t.Fatal(err)
				}
				if s := waitImport(t, c); s.Imported != 2 {
					t.Fatal(s)
				}
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "checkpoint")
			cmd := exec.Command(os.Args[0], "-test.run=^TestImportCrashWorker$", "-test.timeout=45s")
			cmd.Env = append(os.Environ(), "SWD_IMPORT_CRASH_CONFIG="+c.configPath, "SWD_IMPORT_CRASH_MODE="+mode, "SWD_IMPORT_CRASH_MARKER="+marker)
			log, err := os.Create(filepath.Join(filepath.Dir(marker), "child.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			deadline := time.NewTimer(30 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
		wait:
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				select {
				case err := <-exited:
					data, _ := os.ReadFile(log.Name())
					t.Fatalf("child exited before checkpoint: %v\n%s", err, data)
				case <-deadline.C:
					t.Fatal("child did not reach checkpoint")
				case <-tick.C:
					continue wait
				}
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-exited
			if mode == "copy" {
				data, err := os.ReadFile(marker)
				if err != nil {
					t.Fatal(err)
				}
				copied, err := strconv.ParseInt(string(data), 10, 64)
				if err != nil || copied <= 0 || copied >= task.Files[0].File.Size {
					t.Fatal("did not interrupt a partial copy", copied, err)
				}
			}
			c = restartTestConsole(t, c)
			if c.importStatus.Running || c.service != nil {
				t.Fatal("crash restart started work")
			}
			if mode != "cleanup" {
				if err := c.resumeImport(task.ID); err != nil {
					t.Fatal(err)
				}
				s := waitImport(t, c)
				want := 2
				if mode == "published" {
					want = 1
				}
				if s.Imported != want || s.Failed != 0 || s.Checked != 2 {
					t.Fatal(s)
				}
			} else {
				if _, err := os.Stat(task.Files[1].File.Source); err != nil {
					t.Fatal("startup resumed deletion", err)
				}
				if err := c.cleanupImport(task.ID, true); err != nil {
					t.Fatal(err)
				}
				if s := waitImport(t, c); s.Deleted != 1 || s.CleanupAvailable {
					t.Fatal("missing source counted as a deletion", s)
				}
			}
			if mode == "published" && c.importTask.Files[0].Receipt != nil {
				t.Fatal("unrecorded publication got a receipt")
			}
			if mode == "copy" {
				receipt := c.importTask.Files[0].Receipt
				a, err := os.Stat(receipt.Source)
				if err != nil {
					t.Fatal(err)
				}
				b, err := os.Stat(receipt.Destination)
				if err != nil || os.SameFile(a, b) {
					t.Fatal("expected separate cross-volume files", err)
				}
				c = restartTestConsole(t, c)
				if err := c.cleanupImport(task.ID, true); err != nil {
					t.Fatal(err)
				}
				if s := waitImport(t, c); s.Deleted != 2 || s.Error != "" {
					t.Fatal(s)
				}
			}
		})
	}
}

func TestImportRecoveryBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_IMPORT_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_IMPORT_BROWSER_EVIDENCE for real Console integration")
	}
	c, task := prepareImportRecovery(t, "a", "b")
	for _, phase := range []string{"resume", "cleanup"} {
		c = restartTestConsole(t, c)
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "import-recovery-live.mjs"), ui.URL, phase, evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		if phase == "resume" {
			if s := waitImport(t, c); s.Imported != 2 {
				t.Fatal(s)
			}
		} else if c.importStatus.Present {
			t.Fatal("browser did not finish batch")
		}
	}
	for _, item := range task.Files {
		if _, err := os.Stat(item.File.Source); !os.IsNotExist(err) {
			t.Fatal("browser cleanup failed", err)
		}
	}
}
