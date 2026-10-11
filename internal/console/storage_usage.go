package console

import "fmt"

// Caller holds lifecycleMu and no state locks. Both scan entry points use the
// same restored evidence; an empty inventory alone does not prove initialization.
func (c *Console) storageUsageKnown() bool {
	c.inventoryMu.Lock()
	used := c.inventory.Videos > 0 || c.inventory.Bytes > 0 || c.inventory.CoverageKnown
	c.inventoryMu.Unlock()
	c.mu.Lock()
	// Even a failed attempt ran after engine initialization.
	used = used || c.service != nil || c.lastBatch.Total > 0 || !c.batch.Finished.IsZero()
	c.mu.Unlock()
	return used
}

func missingStorageRootError(root string, err error) error {
	return fmt.Errorf("媒体库目录已失联 %s，请恢复目录或重新选择存储位置；不会自动新建空库：%w", root, err)
}
