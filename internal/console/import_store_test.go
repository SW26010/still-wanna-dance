package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/workfiles"
)

func syntheticImportTask(t testing.TB, count int) (*Console, videoImportTask) {
	t.Helper()
	root := t.TempDir()
	task := videoImportTask{Version: 1, ID: 42, Source: filepath.Join(root, "source"), DatabasePath: filepath.Join(root, "library.sqlite"), VideoDir: filepath.Join(root, "videos"), Enumerated: true}
	for i := 0; i < count; i++ {
		task.Files = append(task.Files, videoImportItem{File: cacheproxy.ImportFile{Source: filepath.Join(task.Source, fmt.Sprintf("%05d.mp4", i)), FileID: "0123456789abcdef", Size: 123456789, Modified: 1700000000000000000}})
	}
	c := &Console{configPath: filepath.Join(root, "config.json"), settings: Settings{DatabasePath: task.DatabasePath, VideoDir: task.VideoDir}}
	if err := c.commitImportTask(task); err != nil {
		t.Fatal(err)
	}
	return c, task
}

func TestImportProgressBatchesAndFlushesOnPause(t *testing.T) {
	c, task := syntheticImportTask(t, 67)
	p, err := c.beginImportProgress(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.db.Close() })
	var synchronous int
	if err := p.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatal("receipts require FULL synchronization", synchronous, err)
	}
	for i := 0; i < 63; i++ {
		item := task.Files[i]
		item.Result = "skipped"
		if err := p.record(i, item, false); err != nil {
			t.Fatal(err)
		}
	}
	read := func(want int) videoImportTask {
		t.Helper()
		restored, err := readImportStore(c.files().VideoImport)
		if err != nil || restored.status(c.settings).Checked != want || c.importStatus.Checked != want {
			t.Fatal("durable progress diverged", want, restored.status(c.settings), c.importStatus, err)
		}
		return restored
	}
	read(0) // No synchronization per skipped item.
	item := task.Files[63]
	item.Result = "skipped"
	if err := p.record(63, item, false); err != nil {
		t.Fatal(err)
	}
	read(64)
	item = task.Files[64]
	item.Result = "failed"
	item.Reason = "temporary I/O failure"
	if err := p.record(64, item, false); err != nil {
		t.Fatal(err)
	}
	item = task.Files[65]
	item.Result = "imported"
	key := strings.Repeat("a", 32)
	item.Receipt = &cacheproxy.ImportedVideo{ImportFile: item.File, MD5: key, Destination: filepath.Join(task.VideoDir, key+".mp4")}
	if err := p.record(65, item, true); err != nil {
		t.Fatal(err)
	}
	if restored := read(66); restored.Files[65].Receipt == nil || !c.importStatus.CleanupAvailable {
		t.Fatal("successful receipt was not committed immediately")
	}
	item = task.Files[66]
	item.Result = "skipped"
	if err := p.record(66, item, false); err != nil {
		t.Fatal(err)
	}
	read(66)
	result := error(context.Canceled)
	p.finish(&result)
	if !errors.Is(result, context.Canceled) {
		t.Fatal(result)
	}
	read(67)
	if want := c.importTask.status(c.settings); c.importStatus != want {
		t.Fatal("incremental counters disagree with full summary", c.importStatus, want)
	}
}

func TestImportProgressFailedBatchIsAtomicAndStops(t *testing.T) {
	c, task := syntheticImportTask(t, 3)
	p, err := c.beginImportProgress(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`CREATE TRIGGER fail_progress BEFORE UPDATE ON items WHEN NEW.position=1 BEGIN SELECT RAISE(ABORT, 'injected save failure'); END`); err != nil {
		t.Fatal(err)
	}
	for i, item := range task.Files {
		item.Result = "skipped"
		if err := p.record(i, item, false); err != nil {
			t.Fatal(err)
		}
	}
	result := error(context.Canceled)
	p.finish(&result)
	if result == nil || errors.Is(result, context.Canceled) || !strings.Contains(result.Error(), "保存导入进度失败") {
		t.Fatal("pause concealed a flush failure", result)
	}
	restored, err := readImportStore(c.files().VideoImport)
	if err != nil || !reflect.DeepEqual(task, restored) || c.importStatus.Checked != 0 {
		t.Fatal("failed batch partially published", restored, c.importStatus, err)
	}
}

func TestImportSnapshotPreservesActiveRecoveryFiles(t *testing.T) {
	c, task := syntheticImportTask(t, 1)
	p, err := c.beginImportProgress(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.db.Close() })
	item := task.Files[0]
	item.Result = "skipped"
	if err := p.record(0, item, true); err != nil {
		t.Fatal(err)
	}
	if err := c.saveImportTask(task); err == nil {
		t.Fatal("replaced main file while an open connection retained its WAL")
	}
	restored, err := readImportStore(c.files().VideoImport)
	if err != nil || restored.Files[0].Result != "skipped" {
		t.Fatal("snapshot replacement lost committed progress", restored, err)
	}
}

func TestImportLegacyMigrationIsExplicitAndPreservesReceipts(t *testing.T) {
	c, task := prepareImportRecovery(t, "a", "b")
	if err := c.resumeImport(task.ID); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); s.Imported != 2 {
		t.Fatal(s)
	}
	task = c.importTask.clone()
	if err := c.removeImportState(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	legacy := c.files().State("video-import")
	if err := workfiles.Write(legacy, data); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if c.service != nil || !reflect.DeepEqual(*c.importTask, task) {
		t.Fatal("startup changed legacy task", c.importStatus)
	}
	if _, err := os.Stat(c.files().VideoImport); !os.IsNotExist(err) {
		t.Fatal("startup migrated legacy state", err)
	}
	slot := filepath.Join(c.files().StateDir, workfiles.TempName(c.files().VideoImport))
	if err := os.WriteFile(slot, []byte("occupied slot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.cleanupImport(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); !strings.Contains(s.Error, "保存导入进度失败") || s.Deleted != 0 {
		t.Fatal("failed migration proceeded to delete sources", s)
	}
	if after, err := os.ReadFile(legacy); err != nil || string(after) != string(data) {
		t.Fatal("migration failure damaged legacy record", err)
	}
	if err := os.Remove(slot); err != nil {
		t.Fatal(err)
	}
	if err := c.cleanupImport(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); s.Deleted != 2 || s.CleanupAvailable || s.Error != "" {
		t.Fatal("migration lost cleanup receipts", s)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy state retained after migration", err)
	}
	c = restartTestConsole(t, c)
	if c.importStatus.Deleted != 2 || c.importStatus.Running || c.service != nil {
		t.Fatal(c.importStatus)
	}
}

func TestImportStoreRejectsIncompleteAndUnknownRecords(t *testing.T) {
	for _, mutation := range []string{"DELETE FROM items WHERE position=2", "UPDATE items SET position=3 WHERE position=2", "PRAGMA user_version=999"} {
		t.Run(mutation, func(t *testing.T) {
			c, task := syntheticImportTask(t, 3)
			legacy, err := json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			if err := workfiles.Write(c.files().State("video-import"), legacy); err != nil {
				t.Fatal(err)
			}
			db, err := openImportStore(c.files().VideoImport, true)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(mutation)
			if err := errors.Join(err, db.Close()); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(c.files().VideoImport)
			if err != nil {
				t.Fatal(err)
			}
			c.importTask, c.importStatus = nil, ImportStatus{}
			c.loadImportTask()
			if !c.importStatus.Present || c.importStatus.Error == "" || c.importTask != nil {
				t.Fatal("corrupt record accepted", c.importStatus)
			}
			after, err := os.ReadFile(c.files().VideoImport)
			if err != nil || string(after) != string(before) {
				t.Fatal("recovery modified damaged record", err)
			}
			if err := c.removeImportState(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImportStoreRejectsAliasesAndOrphanedRecoveryFiles(t *testing.T) {
	t.Run("hardlink", func(t *testing.T) {
		c, _ := syntheticImportTask(t, 1)
		alias := filepath.Join(c.files().StateDir, "other.sqlite")
		if err := os.Link(c.files().VideoImport, alias); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(alias)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readImportStore(c.files().VideoImport); err == nil {
			t.Fatal("opened database with independently locked hard-link alias")
		}
		if err := c.removeImportState(); err != nil {
			t.Fatal(err)
		}
		if after, err := os.ReadFile(alias); err != nil || string(before) != string(after) {
			t.Fatal("discard changed aliased content", err)
		}
	})
	t.Run("orphan", func(t *testing.T) {
		c, _ := syntheticImportTask(t, 1)
		if err := os.Remove(c.files().VideoImport); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.files().VideoImport+"-wal", []byte("recovery material"), 0600); err != nil {
			t.Fatal(err)
		}
		c.importTask, c.importStatus = nil, ImportStatus{}
		c.loadImportTask()
		if !c.importStatus.Present || c.importStatus.Error == "" || c.importTask != nil {
			t.Fatal("orphaned recovery files silently ignored", c.importStatus)
		}
		if err := c.removeImportState(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestImportSkipBatchSaveFailureStopsBeforeNextFile(t *testing.T) {
	var names []string
	for i := 0; i < 65; i++ {
		names = append(names, fmt.Sprintf("%03d", i))
	}
	c, task := prepareImportRecovery(t, names...)
	for _, item := range task.Files[:64] {
		if err := os.Remove(item.File.Source); err != nil {
			t.Fatal(err)
		}
	}
	db, err := openImportStore(c.files().VideoImport, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER fail_progress BEFORE UPDATE ON items BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	if err := c.resumeImport(task.ID); err != nil {
		t.Fatal(err)
	}
	if s := waitImport(t, c); s.Checked != 0 || !strings.Contains(s.Error, "保存导入进度失败") {
		t.Fatal("failed batch was published", s)
	}
	entries, err := os.ReadDir(task.VideoDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".mp4") {
			t.Fatal("processed next video after skipped batch failed", entry.Name())
		}
	}
}

func TestImportProgressWriteVolumeDoesNotGrowWithManifest(t *testing.T) {
	var small int64
	for _, count := range []int{1000, 10000} {
		c, task := syntheticImportTask(t, count)
		p, err := c.beginImportProgress(task)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.db.Close() })
		// Keep the WAL so the test measures actual SQLite page writes. Each
		// update is committed separately, exercising the strong receipt policy.
		if _, err := p.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 128; i++ {
			item := task.Files[i]
			item.Result, item.Reason = "skipped", "目标已存在、资源未知或已屏蔽"
			if err := p.record(i, item, true); err != nil {
				t.Fatal(err)
			}
		}
		info, err := os.Stat(c.files().VideoImport + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		bytes := info.Size()
		t.Logf("manifest=%d, 128 individually committed updates: WAL=%d bytes", count, bytes)
		if bytes > 128*32*1024 {
			t.Fatal("per-item writes exceeded bounded page budget", bytes)
		}
		if small == 0 {
			small = bytes
		} else if bytes > small*2 {
			t.Fatal("updating fixed items scales with the whole manifest", small, bytes)
		}
	}
}

func TestImportSkippedBatchWriteVolumeScalesLinearly(t *testing.T) {
	var small int64
	for _, count := range []int{1000, 2000, 10000} {
		c, task := syntheticImportTask(t, count)
		p, err := c.beginImportProgress(task)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.db.Close() })
		if _, err := p.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
		for i, item := range task.Files {
			item.Result, item.Reason = "skipped", "目标已存在、资源未知或已屏蔽"
			if err := p.record(i, item, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.flush(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(c.files().VideoImport + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		bytes := info.Size()
		t.Logf("%d skipped items: WAL=%d bytes", count, bytes)
		if c.importStatus.Skipped != count || c.importStatus.Paused || bytes > int64(count)*4096 {
			t.Fatal("progress or write budget regression", c.importStatus, bytes)
		}
		if small == 0 {
			small = bytes
		} else if bytes > small*int64(count/1000)*12/10 {
			t.Fatal("whole-task writes grew faster than linear budget", small, bytes)
		}
	}
}

func BenchmarkImportItemPublication(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			c, task := syntheticImportTask(b, count)
			updates := []importItemUpdate{{index: 0, item: task.Files[0]}}
			updates[0].item.Result = "skipped"
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				c.publishImportItems(updates)
			}
		})
	}
}
