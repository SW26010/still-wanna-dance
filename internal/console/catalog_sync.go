package console

import (
	"context"
	"time"

	"still-wanna-dance/internal/catalogsync"
)

// Caller holds c.mu. Business progress is shared by both detection triggers.
func (c *Console) metadataLocked() *catalogsync.Sync {
	if c.metadata == nil {
		c.metadata = catalogsync.New(catalogsync.Options{
			SongCandidates: c.metadataSongCandidates,
		})
	}
	return c.metadata
}
func (c *Console) catalogDataUpdated() {
	c.mu.Lock()
	m := c.metadata
	c.mu.Unlock()
	if m != nil {
		m.Updated()
	}
}

// Keep the catalog status API as a projection of the shared detection lifecycle.
type catalogSyncStatus struct {
	Initializing bool      `json:"initializing"`
	Checking     bool      `json:"checking"`
	Finished     time.Time `json:"finished"`
	Updated      time.Time `json:"updated"`
	NextCheck    time.Time `json:"nextCheck"`
	Error        string    `json:"error,omitempty"`
}

func (c *Console) catalogSyncSnapshotLocked() catalogSyncStatus {
	var s catalogSyncStatus
	if c.metadata != nil {
		s.Updated = c.metadata.UpdatedAt()
	}
	if n := c.networkDiagnostics; n != nil {
		v := n.connectivity
		s.Initializing, s.Checking = v.Initializing && !v.Closed, v.Running
		s.Finished, s.NextCheck = v.Finished, v.NextCheck
		if s.Initializing && !s.Finished.IsZero() {
			s.Error = "尚未取得可用的完整歌曲清单"
		}
	}
	return s
}

// The accepted songs watermark, not seed mappings or imported files, completes
// initialization. Active storage is fixed and remains open until metadata synchronization stops.
func (c *Console) catalogReady(ctx context.Context) bool {
	store, err := c.businessStore()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := store.CatalogStatus(ctx)
	if err != nil || status.Revision == "" {
		return false
	}
	c.startInventory(false)
	return true
}
