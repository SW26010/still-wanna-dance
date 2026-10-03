package console

import (
	"os"
	"testing"
	"time"
)

func TestBatchBudgetDownloadsHighestInitialPriorities(t *testing.T) {
	c, _, body, _ := queueRetentionFixture(t, 2*int64(len("audit video content")))
	defer c.Close()
	c.scanPlan = &scanPlan{settings: c.settings, songs: []Song{{ID: 999999}, {ID: 5404}, {ID: 999998}, {ID: 1981}}, results: map[int64]scanResult{}}
	if err := c.startBatch(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch stalled")
	}
	if c.batch.Downloaded != 2 || !c.batch.BudgetReached || c.batch.Failed != 0 {
		t.Fatalf("%+v", c.batch)
	}
	for _, id := range []string{"1981", "5404"} {
		if _, err := os.Stat(fixtureVideoPath(c.settings.StorageDir, id, body)); err != nil {
			t.Fatalf("priority song %s missing: %v", id, err)
		}
	}
	for _, id := range []string{"999998", "999999"} {
		if _, err := os.Stat(fixtureVideoPath(c.settings.StorageDir, id, body)); !os.IsNotExist(err) {
			t.Fatalf("low priority song %s downloaded: %v", id, err)
		}
	}
}
