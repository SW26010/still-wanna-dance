package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	clients := c.operationClients(ctx, upstreamstate.Catalog, upstreamstate.Constraints{Entry: c.checksumURL})
	return upstreamrequest.Fetch(ctx, c.checksumURL, clients, 30*time.Second, c.fetchCatalogWithClient)
}

func (c *Console) fetchCatalogWithClient(ctx context.Context, client *http.Client, target string) (cacheproxy.Catalog, error) {
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
	candidate, err := cacheproxy.ParseCatalog(body, c.checksumURL)
	if err != nil {
		return candidate, err
	}
	return candidate, nil
}
