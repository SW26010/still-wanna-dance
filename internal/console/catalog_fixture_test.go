package console

import (
	"context"
	"still-wanna-dance/internal/cacheproxy"
	"strconv"
)

func syncTestCatalog(c *Console, s *cacheproxy.Server, ctx context.Context, refs map[string]string) error {
	source := c.checksumURL
	if source == "" {
		source = "test"
	}
	catalog := cacheproxy.Catalog{Source: source, Revision: "20000101000000"}
	for id, key := range refs {
		n, _ := strconv.ParseInt(id, 10, 64)
		catalog.Songs = append(catalog.Songs, cacheproxy.CatalogSong{ID: n, MD5: key})
	}
	return s.SyncCatalog(ctx, catalog)
}
