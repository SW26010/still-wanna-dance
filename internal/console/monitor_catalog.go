package console

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

// Reuse probe bodies; parsing happens outside storage lifecycle locks.
func (c *Console) syncMonitorCatalogs(ctx context.Context, responses []upstreamstate.CatalogResponse) {
	type candidate struct {
		full             *cacheproxy.Catalog
		revision, source string
		at               time.Time
	}
	var candidates []candidate
	var failures []error
	seen := make(map[[32]byte]bool)
	for _, response := range responses {
		if ctx.Err() != nil {
			return
		}
		// Deduplicate identical bodies; semantic duplicates are handled by storage.
		key := sha256.Sum256(append([]byte(response.Route+"\x00"), response.Body...))
		if seen[key] {
			continue
		}
		seen[key] = true
		var v candidate
		var err error
		switch response.Route {
		case "kiva", "wanna":
			var full cacheproxy.Catalog
			full, err = cacheproxy.ParseCatalog(response.Body, response.Source)
			v.full, v.revision = &full, full.Revision
		default:
			continue
		}
		if err == nil {
			v.at, err = cacheproxy.ParseCatalogTime(v.revision)
		}
		if err != nil {
			slog.Warn("monitor_catalog_invalid", "source", response.Source, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", response.Source, err))
			continue
		}
		v.source = response.Source
		candidates = append(candidates, v)
	}
	if (len(candidates) == 0 && len(failures) == 0) || ctx.Err() != nil {
		return
	}
	// Only MD5 catalogs are business inputs; offer newer versions first.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].at.After(candidates[j].at)
	})
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.closing || ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	err := c.ensureEngine()
	engine := c.service
	c.mu.Unlock()
	if err != nil {
		slog.Warn("monitor_catalog_sync_failed", "error", err)
		return
	}
	for _, v := range candidates {
		if ctx.Err() != nil {
			return
		}
		err = engine.SyncCatalog(ctx, *v.full)
		if err != nil && !errors.Is(err, cacheproxy.ErrCatalogOlder) {
			slog.Warn("monitor_catalog_sync_failed", "source", v.source, "revision", v.revision, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", v.source, err))
		}
	}
	// Record after the entire round so a later duplicate or older mirror cannot
	// hide a conflict from another candidate in this same round.
	if len(failures) > 0 && ctx.Err() == nil {
		if err := engine.RecordCatalogError(ctx, errors.Join(failures...)); err != nil {
			slog.Warn("monitor_catalog_status_failed", "error", err)
		}
	}
}
