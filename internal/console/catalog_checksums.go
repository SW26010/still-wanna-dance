package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Fetching only feeds synchronization. Business operations read the database
// after this attempt, including when the remote endpoint is unavailable.
func (c *Console) refreshLocalCatalog(ctx context.Context, engine *cacheproxy.Server) error {
	candidate, err := c.fetchCatalogSnapshot(ctx)
	if err == nil {
		err = engine.SyncCatalog(ctx, candidate)
	}
	if c.checksumURL == cacheproxy.UdonCatalogURL {
		missing, checkErr := engine.CatalogNeedsMappings(ctx)
		if checkErr != nil {
			return checkErr
		}
		if err != nil || missing {
			var backups []cacheproxy.Catalog
			var failures []error
			if errors.Is(err, cacheproxy.ErrLocalStorage) {
				return err
			}
			if errors.Is(err, cacheproxy.ErrCatalogConflict) {
				failures = append(failures, err)
			}
			for _, source := range []string{cacheproxy.KivaCatalogURL, cacheproxy.WannaCatalogURL} {
				backup, fetchErr := c.fetchCatalogSource(ctx, source)
				if fetchErr != nil {
					failures = append(failures, fetchErr)
					slog.Warn("catalog_backup_failed", "source", source, "error", fetchErr)
					continue
				}
				backups = append(backups, backup)
			}
			sort.SliceStable(backups, func(i, j int) bool { return backups[i].Revision > backups[j].Revision })
			usable := err == nil || errors.Is(err, cacheproxy.ErrCatalogOlder)
			for _, backup := range backups {
				syncErr := engine.SyncCatalog(ctx, backup)
				if syncErr == nil || errors.Is(syncErr, cacheproxy.ErrCatalogOlder) {
					usable = true
				} else {
					failures = append(failures, syncErr)
				}
			}
			if usable {
				err = nil
			} else {
				err = errors.Join(append([]error{err}, failures...)...)
			}
			// Keep conflicts visible even when another source was usable.
			for _, failure := range failures {
				if errors.Is(failure, cacheproxy.ErrCatalogConflict) {
					err = errors.Join(err, failure)
				}
			}
		}
	}
	if errors.Is(err, cacheproxy.ErrCatalogOlder) {
		return nil
	}
	if err != nil && ctx.Err() == nil {
		if saveErr := engine.RecordCatalogError(ctx, err); saveErr != nil {
			return fmt.Errorf("%v；无法保存检查结果：%w", err, saveErr)
		}
	}
	return err
}

func (c *Console) fetchCatalogSnapshot(ctx context.Context) (cacheproxy.Catalog, error) {
	return c.fetchCatalogSource(ctx, c.checksumURL)
}

func (c *Console) fetchCatalogSource(ctx context.Context, source string) (cacheproxy.Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	clients := c.operationClients(ctx, upstreamstate.Catalog, upstreamstate.Constraints{Entry: source})
	return upstreamrequest.Fetch(ctx, source, clients, 30*time.Second, func(ctx context.Context, client *http.Client, target string) (cacheproxy.Catalog, error) {
		return fetchCatalogResponse(ctx, client, target, source)
	})
}

func (c *Console) fetchCatalogWithClient(ctx context.Context, client *http.Client, target string) (cacheproxy.Catalog, error) {
	return fetchCatalogResponse(ctx, client, target, c.checksumURL)
}

func fetchCatalogResponse(ctx context.Context, client *http.Client, target, source string) (cacheproxy.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return cacheproxy.Catalog{}, applog.SafeError(err)
	}
	resp, err := upstreamrequest.Do(client, req)
	if err != nil {
		return cacheproxy.Catalog{}, applog.SafeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cacheproxy.Catalog{}, fmt.Errorf("清单接口返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return cacheproxy.Catalog{}, err
	}
	if len(body) > 16<<20 {
		return cacheproxy.Catalog{}, fmt.Errorf("清单过大")
	}
	candidate, err := cacheproxy.ParseCatalog(body, source)
	if err != nil {
		return candidate, err
	}
	return candidate, nil
}
