package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Fetching only feeds synchronization. Business operations read the database
// after this attempt, including when the remote endpoint is unavailable.
func (c *Console) refreshLocalCatalog(ctx context.Context, store *cacheproxy.Store) error {
	sources := []string{c.checksumURL}
	if cacheproxy.IsUdonCatalog(c.checksumURL) {
		for _, route := range catalogmeta.SourceIDs()[1:] {
			sources = append(sources, catalogmeta.SourceURL(route))
		}
	}
	var failures []error
	for _, source := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		candidate, err := c.fetchCatalogSource(ctx, source)
		if err == nil {
			err = store.SyncCatalog(ctx, candidate)
		}
		if err == nil || errors.Is(err, cacheproxy.ErrCatalogOlder) {
			return nil
		}
		if errors.Is(err, cacheproxy.ErrLocalStorage) {
			return err
		}
		failures = append(failures, err)
		slog.Warn("catalog_source_failed", "source", source, "error", err)
	}
	err := errors.Join(failures...)
	if err != nil && ctx.Err() == nil {
		if saveErr := store.RecordCatalogError(ctx, err); saveErr != nil {
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
