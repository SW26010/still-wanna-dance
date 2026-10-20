package console

import (
	"errors"
	"fmt"

	"still-wanna-dance/internal/cacheproxy"
)

// Caller holds no state locks, and holds lifecycleMu before acting on the result.
// Storage entry points use the same restored evidence; an empty inventory alone
// does not prove initialization.
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

// inspectStorage applies the Console's restored usage evidence to the read-only
// layout check. A different target or an unused current target may be initialized.
// Caller holds no state locks. Mutating callers hold lifecycleMu across this
// check and the operation; read-only setup hints may inspect without it.
func (c *Console) inspectStorage(root string) (bool, error) {
	existing, err := cacheproxy.InspectStorage(root)
	if existing || err != nil {
		return existing, err
	}
	c.mu.Lock()
	activeRoot := c.settings.StorageDir
	c.mu.Unlock()
	if root == activeRoot && c.storageUsageKnown() {
		return false, errors.New("当前已使用的存储库失联或数据库缺失，请恢复原目录与数据库")
	}
	return false, nil
}

func missingStorageRootError(root string, err error) error {
	return fmt.Errorf("媒体库目录已失联 %s，请恢复目录或重新选择存储位置；不会自动新建空库：%w", root, err)
}
