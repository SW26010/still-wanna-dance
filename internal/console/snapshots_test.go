package console

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

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
	dir := filepath.Join(c.settings.StorageDir, "videos")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtureVideoPath(c.settings.StorageDir, "1", "video"), []byte("video"), 0600); err != nil {
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
	restarted, err := New(c.configPath, c.address)
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
	if err := os.Remove(fixtureVideoPath(c.settings.StorageDir, "1", "video")); err != nil {
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
	newSettings.StorageDir += "-different"
	if err := c.save(newSettings); err != nil {
		t.Fatal(err)
	}
	if !c.inventory.Updated.IsZero() {
		t.Fatal("reused snapshot for a different library")
	}
}

func TestScanOnlyPersistsSuccessAcrossFailureAndCancellation(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	const body = "video"
	if err := os.MkdirAll(filepath.Join(c.settings.StorageDir, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtureVideoPath(c.settings.StorageDir, "1", body), []byte(body), 0600); err != nil {
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
	if !c.lastBatch.Updated.After(before.Updated) || c.lastBatch.Hits != 2 || c.lastBatch.CatalogWarning == "" {
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
	restarted, err := New(c.configPath, c.address)
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
