package console

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Only combine snapshots from the same published catalog revision. Missing or
// ambiguous checksums fall back to the authoritative per-song playback API.
func (c *Console) addCatalogChecksums(ctx context.Context, songs []Song, stamp string) {
	checksums, revision, err := c.fetchCatalogChecksums(ctx)
	if err != nil || revision != stamp {
		slog.Warn("scan_checksums_unavailable_or_stale")
		return
	}
	for i := range songs {
		songs[i].Checksum = checksums[songs[i].ID]
	}
}

func (c *Console) fetchCatalogChecksums(ctx context.Context) (map[int64]string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.checksumURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("清单接口返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > 16<<20 {
		return nil, "", fmt.Errorf("清单过大")
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
	if json.Unmarshal(body, &catalog) != nil || catalog.Code != 200 || catalog.Data.Time == "" {
		return nil, "", fmt.Errorf("清单格式无效")
	}
	checksums := map[int64]string{}
	seen := map[int64]bool{}
	for _, group := range catalog.Data.Groups {
		for _, entry := range group.Entries {
			if entry.ID <= 0 {
				return nil, "", fmt.Errorf("清单曲目 ID 无效")
			}
			checksum := ""
			digest, err := hex.DecodeString(entry.Checksum)
			if err == nil && len(digest) == 16 {
				checksum = strings.ToLower(entry.Checksum)
			}
			if seen[entry.ID] {
				// Conflicts stay invalid even if a later entry matches an earlier one.
				if checksums[entry.ID] != checksum {
					checksums[entry.ID] = ""
				}
				continue
			}
			seen[entry.ID] = true
			checksums[entry.ID] = checksum
		}
	}
	if len(checksums) == 0 {
		return nil, "", fmt.Errorf("清单为空")
	}
	return checksums, catalog.Data.Time, nil
}
