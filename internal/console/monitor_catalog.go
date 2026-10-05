package console

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

// Reuse probe bodies; parsing happens outside storage lifecycle locks.
func (c *Console) syncMonitorCatalogs(ctx context.Context, responses []upstreamstate.CatalogResponse) {
	type candidate struct {
		full             *cacheproxy.Catalog
		names            map[string]string
		revision, source string
		at               time.Time
	}
	var candidates []candidate
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
		case "api":
			var names songCatalog
			names, err = parseCatalog(bytes.NewReader(response.Body))
			v.revision = names.Revision
			v.names = make(map[string]string, len(names.Songs))
			for _, song := range names.Songs {
				v.names[strconv.FormatInt(song.ID, 10)] = song.Name
			}
		default:
			continue
		}
		if err == nil {
			v.at, err = cacheproxy.ParseCatalogTime(v.revision)
		}
		if err != nil {
			slog.Warn("monitor_catalog_invalid", "source", response.Source, "error", err)
			continue
		}
		v.source = response.Source
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 || ctx.Err() != nil {
		return
	}
	// Full metadata first so a newer names-only watermark cannot block this
	// batch's MD5 data. Within each format, offer newer versions first.
	sort.SliceStable(candidates, func(i, j int) bool {
		if (candidates[i].full != nil) != (candidates[j].full != nil) {
			return candidates[i].full != nil
		}
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
		if v.full != nil {
			err = engine.SyncCatalog(ctx, *v.full)
		} else {
			err = engine.SyncCatalogNames(ctx, v.revision, v.source, v.names)
		}
		if err != nil {
			slog.Warn("monitor_catalog_sync_failed", "source", v.source, "revision", v.revision, "error", err)
		}
	}
}
