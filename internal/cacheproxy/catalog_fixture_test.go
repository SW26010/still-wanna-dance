package cacheproxy

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"
)

var catalogTestRevision atomic.Int64

func syncTestCatalog(s *Server, ctx context.Context, refs map[string]string) error {
	c := Catalog{Source: "test", Revision: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(catalogTestRevision.Add(1)) * time.Second).Format("20060102150405")}
	for id, key := range refs {
		n, _ := strconv.ParseInt(id, 10, 64)
		c.Songs = append(c.Songs, CatalogSong{ID: n, MD5: key})
	}
	return s.SyncCatalog(ctx, c)
}
