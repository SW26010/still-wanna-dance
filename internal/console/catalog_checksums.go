package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"still-wanna-dance/internal/cacheproxy"
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
	var failures []error
	clients := c.operationClients(upstreamstate.Catalog, upstreamstate.Constraints{Entry: c.checksumURL})
	for i, client := range clients {
		if err := ctx.Err(); err != nil {
			return cacheproxy.Catalog{}, err
		}
		attempt, stop := channelAttempt(ctx, 30*time.Second, len(clients)-i)
		catalog, err := c.fetchCatalogWithClient(attempt, client)
		stop()
		if err == nil {
			return catalog, nil
		}
		failures = append(failures, err)
	}
	return cacheproxy.Catalog{}, errors.Join(failures...)
}

func (c *Console) fetchCatalogWithClient(ctx context.Context, client *http.Client) (cacheproxy.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.checksumURL, nil)
	if err != nil {
		return cacheproxy.Catalog{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return cacheproxy.Catalog{}, err
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
