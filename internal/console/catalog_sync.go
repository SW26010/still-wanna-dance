package console

import (
	"context"
	"errors"
	"fmt"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/catalogsync"
)

func catalogSourceError(source string, err error) error {
	name := "清单来源"
	switch {
	case source == catalogmeta.UdonHTTPURL:
		name = "备用来源 Udon HTTP"
	case cacheproxy.IsPrimaryCatalog(source):
		name = "主来源 Udon"
	case source == cacheproxy.KivaCatalogURL:
		name = "备用来源 Kiva"
	case source == cacheproxy.WannaCatalogURL:
		name = "备用来源 WannaInfo"
	}
	return fmt.Errorf("%s：%w", name, err)
}

// Report source errors only when the round has no usable response. Successful
// fallback is silent; each failed source still has its diagnostic log.
func catalogRefreshError(failures []error) error {
	err := errors.Join(failures...)
	if err == nil {
		return nil
	}
	return fmt.Errorf("在线曲库更新失败：%w", err)
}

// Caller holds c.mu. Business progress is shared by both detection triggers.
func (c *Console) metadataLocked() *catalogsync.Sync {
	if c.metadata == nil {
		c.metadata = catalogsync.New(catalogsync.Options{
			SongCandidates: c.metadataSongCandidates,
		})
	}
	return c.metadata
}
func (c *Console) catalogChecked(err error) {
	c.mu.Lock()
	m := c.metadataLocked()
	c.mu.Unlock()
	m.CatalogChecked(err)
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
		s.Error = c.metadata.CatalogError()
	}
	if c.store != nil {
		s.Updated = c.store.MetadataUpdatedAt()
	}
	if n := c.networkDiagnostics; n != nil {
		v, _ := n.connectivitySnapshot()
		s.Initializing, s.Checking = v.Initializing && !v.Closed, v.Running
		s.Finished, s.NextCheck = v.Finished, v.NextCheck
		if s.Error == "" && s.Initializing && !s.Finished.IsZero() {
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
