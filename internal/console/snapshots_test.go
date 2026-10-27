package console

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/workfiles"
)

func occupySnapshotSlot(t *testing.T, c *Console, kind string) []byte {
	t.Helper()
	path := c.files().State(kind)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), workfiles.TempName(path)), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	return before
}

func TestInventoryWriteFailurePublishesCompletedMemoryResult(t *testing.T) {
	c := testConsole(t)
	c.startInventoryScan()
	first := waitInventory(t, c)
	if first.Error != "" || first.Videos != 0 || first.Updated.IsZero() {
		t.Fatal(first)
	}
	before := occupySnapshotSlot(t, c, "inventory")
	if err := os.WriteFile(fixtureVideoPath(testStorageRoot(c.settings), "1", "video"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	result := waitInventory(t, c)
	if result.Scanning || result.Videos != 1 || result.Bytes != 5 || result.Updated.IsZero() || !strings.Contains(result.Error, "无法保存") {
		t.Fatal("a failed snapshot hid the new scan result", result)
	}
	if after, err := os.ReadFile(c.files().State("inventory")); err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed write replaced the last saved result", err)
	}
	c = restartTestConsole(t, c)
	if c.inventory.Videos != 0 || !c.inventory.Updated.Equal(first.Updated) || c.inventory.Scanning || c.inventoryDone != nil {
		t.Fatal("restart invented a saved result or resumed a scan", c.inventory)
	}
}

func TestBatchWriteFailurePublishesSuccessWithoutReplacingSavedResult(t *testing.T) {
	c := testConsole(t)
	var count atomic.Int32
	count.Store(1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Load()
		songs := map[int]string{1: "video"}
		if n == 2 {
			songs[2] = "video"
		}
		fmt.Fprint(w, testMD5CatalogBody(fmt.Sprintf("2026102600000%d", n), songs))
	}))
	defer api.Close()
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	waitMD5Batch(t, c, true)
	if c.lastBatch.Total != 1 || c.lastBatch.Failed != 0 {
		t.Fatal(c.lastBatch)
	}
	before := map[string][]byte{}
	for _, kind := range []string{"batch-last-success", "batch-last-attempt"} {
		before[kind] = occupySnapshotSlot(t, c, kind)
	}
	count.Store(2)
	waitMD5Batch(t, c, true)
	if c.batch.Total != 2 || c.lastBatch.Total != 2 || c.lastBatch.Failed != 0 || c.batch.Running || !strings.Contains(c.batch.Phase, "未能保存") || !strings.Contains(c.lastBatch.Phase, "未能保存") {
		t.Fatal("snapshot failures hid completed work", c.batch, c.lastBatch)
	}
	for kind, saved := range before {
		if after, err := os.ReadFile(c.files().State(kind)); err != nil || !bytes.Equal(saved, after) {
			t.Fatal("failed write replaced a saved result", kind, err)
		}
	}
	c = restartTestConsole(t, c)
	if c.batch.Total != 1 || c.lastBatch.Total != 1 || c.batch.Running || c.batchDone != nil {
		t.Fatal("restart invented saved work or resumed a task", c.batch, c.lastBatch)
	}
}

func waitInventory(t *testing.T, c *Console) Inventory {
	t.Helper()
	c.inventoryMu.Lock()
	done := c.inventoryDone
	c.inventoryMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("inventory timed out")
		}
	}
	return c.localInventory()
}

func TestInventoryPersistsAndKeepsLastSuccessOnFailure(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(testStorageRoot(c.settings), "videos")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtureVideoPath(testStorageRoot(c.settings), "1", "video"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	before := waitInventory(t, c)
	if before.Videos != 1 || before.Error != "" {
		t.Fatalf("%+v", before)
	}
	if err := os.Rename(dir, dir+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	after := waitInventory(t, c)
	if after.Videos != 1 || after.Updated != before.Updated || after.Error == "" {
		t.Fatalf("overwrote successful inventory: %+v", after)
	}
	restarted, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	saved := restarted.localInventory()
	if saved.Videos != 1 || !saved.Updated.Equal(before.Updated) || saved.Scanning {
		t.Fatalf("did not restore snapshot: %+v", saved)
	}
	if err := os.Rename(dir+"-unavailable", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixtureVideoPath(testStorageRoot(c.settings), "1", "video")); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	after = waitInventory(t, c)
	// Windows may report equal timestamps for two scans within one clock tick.
	// Changed contents prove replacement; time must not move backwards.
	if after.Videos != 0 || after.Error != "" || after.Updated.Before(before.Updated) {
		t.Fatalf("did not replace successful snapshot: %+v", after)
	}
	newSettings := c.settings
	setTestLibraryPaths(&newSettings, testStorageRoot(newSettings)+"-different")
	createTestDatabase(t, newSettings.DatabasePath)
	if err := c.save(newSettings); err != nil {
		t.Fatal(err)
	}
	if c.inventory.Updated.IsZero() {
		t.Fatal("save discarded active inventory")
	}
	c = restartTestConsole(t, c)
	if !c.inventory.Updated.IsZero() {
		t.Fatal("reused snapshot for a different library")
	}
}

func TestScanOnlyPersistsSuccessAcrossFailureAndCancellation(t *testing.T) {
	c := testConsole(t)
	c.settings.VideoStorageMode = "unlimited" // Preserve the videos underlying the saved scan result.
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	const body = "video"
	initializeTestLibrary(t, c)
	if err := os.MkdirAll(filepath.Join(testStorageRoot(c.settings), "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtureVideoPath(testStorageRoot(c.settings), "1", body), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	started := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 1 {
			http.Error(w, "offline", 503)
			return
		}
		if mode.Load() == 2 {
			close(started)
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/catalog" {
			writeTestMD5Catalog(w, map[int]string{1: body, 2: body})
			return
		}
		w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/%s-abc.mp4?e=%x&s=%d", r.URL.Query().Get("id"), md5.Sum([]byte(body)), len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	defer c.Close()
	c.apiBase = api.URL
	c.checksumURL = api.URL + "/catalog"
	c.client.Transport = http.DefaultTransport
	wait := func() {
		t.Helper()
		c.mu.Lock()
		done := c.batchDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("batch timed out")
		}
	}
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	wait()
	before := c.lastBatch
	if before.Total != 2 || before.Hits != 2 || before.Missing != 0 || before.Downloaded != 0 || before.Updated.IsZero() {
		t.Fatalf("scan started engine/downloads or missing snapshot: %+v", before)
	}
	mode.Store(1)
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	wait()
	// Windows clock resolution can give both successful scans the same timestamp.
	// The new warning proves the offline result replaced the previous snapshot.
	if c.lastBatch.Updated.Before(before.Updated) || c.lastBatch.Hits != 2 || c.lastBatch.CatalogWarning == "" {
		t.Fatal("offline scan did not use local database")
	}
	before = c.lastBatch
	mode.Store(2)
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("scan did not start")
	}
	c.mu.Lock()
	c.batchCancel()
	c.mu.Unlock()
	wait()
	if c.lastBatch.Updated != before.Updated {
		t.Fatal("cancellation overwrote result")
	}
	restarted, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.lastBatch.Hits != 2 || restarted.lastBatch.Missing != 0 || !restarted.lastBatch.Updated.Equal(before.Updated) || restarted.batch.Running {
		t.Fatalf("restart lost result: %+v", restarted.lastBatch)
	}
	if restarted.batch.Finished.IsZero() || restarted.batch.Phase != c.batch.Phase {
		t.Fatal("restart lost the cancelled task record")
	}
}

func TestScanCanRunAlongsideQueue(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer api.Close()
	defer c.Close()
	c.apiBase = api.URL
	c.checksumURL = api.URL + "/catalog"
	c.client.Transport = http.DefaultTransport
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.queue.Running || !c.batch.Running || !c.batch.ScanOnly {
		t.Fatal("scan and queue should remain running")
	}
}
