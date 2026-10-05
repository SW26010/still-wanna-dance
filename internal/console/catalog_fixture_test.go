package console

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"still-wanna-dance/internal/cacheproxy"
	"strconv"
)

func testMD5CatalogBody(revision string, songs map[int]string) string {
	entries := make([]map[string]any, 0, len(songs))
	for id, body := range songs {
		entries = append(entries, map[string]any{"id": id, "name": fmt.Sprint(id), "checksum": fmt.Sprintf("%x", md5.Sum([]byte(body)))})
	}
	body, _ := json.Marshal(map[string]any{"code": 200, "data": map[string]any{"time": revision, "groups": []any{map[string]any{"entries": entries}}}})
	return string(body)
}

func writeTestMD5Catalog(w http.ResponseWriter, songs map[int]string) {
	fmt.Fprint(w, testMD5CatalogBody("20261004235822", songs))
}

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
