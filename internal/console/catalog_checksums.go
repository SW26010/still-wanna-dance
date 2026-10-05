package console

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) fetchCatalogSnapshot(ctx context.Context) (cacheproxy.Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.checksumURL, nil)
	if err != nil {
		return cacheproxy.Catalog{}, err
	}
	resp, err := c.upstreamClient().Do(req)
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
