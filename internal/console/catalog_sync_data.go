package console

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"sort"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamcheck"
)

// Reuse probe bodies; parsing happens outside storage lifecycle locks.
func (c *Console) syncCatalogs(ctx context.Context, responses []upstreamcheck.CatalogResponse) (usable bool, resultErr error) {
	defer func() {
		if ctx.Err() == nil && (usable || resultErr != nil) {
			c.catalogChecked(resultErr)
		}
	}()
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
			return usable, ctx.Err()
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
		case "api", "api-http", "kiva", "wanna":
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
			slog.Warn("catalog_sync_invalid", "source", response.Source, "error", err)
			failures = append(failures, catalogSourceError(response.Source, err))
			continue
		}
		v.source = response.Source
		candidates = append(candidates, v)
	}
	if (len(candidates) == 0 && len(failures) == 0) || ctx.Err() != nil {
		return usable, ctx.Err()
	}
	// Offer newer versions first, preferring HTTPS Udon at the same version.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].at.Equal(candidates[j].at) {
			return cacheproxy.IsPrimaryCatalog(candidates[i].source) && !cacheproxy.IsPrimaryCatalog(candidates[j].source)
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
	store, err := c.businessStore()
	c.mu.Unlock()
	if err != nil {
		slog.Warn("catalog_sync_sync_failed", "error", err)
		return false, err
	}
	for _, v := range candidates {
		if ctx.Err() != nil {
			return usable, ctx.Err()
		}
		err = store.SyncCatalog(ctx, *v.full)
		if err != nil && !errors.Is(err, cacheproxy.ErrCatalogOlder) {
			slog.Warn("catalog_sync_sync_failed", "source", v.source, "revision", v.revision, "error", err)
			if errors.Is(err, cacheproxy.ErrLocalStorage) {
				return false, err
			}
			failures = append(failures, catalogSourceError(v.source, err))
		} else {
			usable = true
		}
	}
	if usable {
		if ctx.Err() == nil {
			c.catalogDataUpdated()
		}
		return usable, ctx.Err()
	}
	return false, errors.Join(catalogRefreshError(failures), ctx.Err())
}
