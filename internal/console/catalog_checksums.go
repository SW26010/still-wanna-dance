package console

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Only combine snapshots from the same published catalog revision. Missing or
// ambiguous checksums fall back to the authoritative per-song playback API.
func (c *Console) addCatalogChecksums(ctx context.Context, songs []Song, stamp string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.checksumURL, nil)
	if err != nil {
		return
	}
	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("scan_checksums_unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(body) > 16<<20 {
		return
	}
	var catalog struct {
		Code int `json:"code"`
		Data struct {
			Time   string `json:"time"`
			Groups []struct {
				Entries []struct {
					ID       int64  `json:"id"`
					Checksum string `json:"checksum"`
				} `json:"entries"`
			} `json:"groups"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Code != 200 || catalog.Data.Time != stamp {
		return
	}
	checksums := map[int64]string{}
	seen := map[int64]bool{}
	for _, group := range catalog.Data.Groups {
		for _, entry := range group.Entries {
			if seen[entry.ID] {
				checksums[entry.ID] = ""
				continue
			}
			seen[entry.ID] = true
			digest, err := hex.DecodeString(entry.Checksum)
			if err == nil && len(digest) == 16 {
				checksums[entry.ID] = strings.ToLower(entry.Checksum)
			}
		}
	}
	for i := range songs {
		songs[i].Checksum = checksums[songs[i].ID]
	}
}
