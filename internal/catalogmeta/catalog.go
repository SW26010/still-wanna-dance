// Package catalogmeta validates the common versioned MD5 catalog envelope.
// Consumers may sample identities or parse full song metadata independently.
package catalogmeta

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidMapping = errors.New("清单 ID 或 MD5 无效或重复")

type Entry struct {
	ID       int64           `json:"id"`
	Checksum string          `json:"checksum"`
	Raw      json.RawMessage `json:"-"`
}

type Catalog struct {
	Revision string
	Entries  []Entry
}

// ParseTime treats the upstream civil timestamp as a common comparison axis;
// UTC is not a claim about the publisher's time zone.
func ParseTime(value string) (time.Time, error) {
	if len(value) != 14 {
		return time.Time{}, fmt.Errorf("清单时间必须为 YYYYMMDDhhmmss")
	}
	t, err := time.Parse("20060102150405", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("清单时间无效: %w", err)
	}
	return t, nil
}

func ValidIdentity(id int64, checksum string) bool {
	digest, err := hex.DecodeString(checksum)
	return id > 0 && err == nil && len(digest) == 16
}

// Parse rejects duplicate IDs even when their checksums match. Different IDs
// may legitimately reference the same content. Optional song fields are opaque.
func Parse(body []byte) (Catalog, error) {
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Time   string `json:"time"`
			Groups []struct {
				Entries []json.RawMessage `json:"entries"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Catalog{}, fmt.Errorf("清单格式无效: %w", err)
	}
	c := Catalog{Revision: envelope.Data.Time}
	if envelope.Code != 200 || envelope.Data.Groups == nil {
		return c, fmt.Errorf("清单格式无效")
	}
	seen := make(map[int64]bool)
	for _, group := range envelope.Data.Groups {
		if group.Entries == nil {
			return c, fmt.Errorf("清单缺少 entries")
		}
		for _, raw := range group.Entries {
			var entry Entry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return c, fmt.Errorf("%w: %v", ErrInvalidMapping, err)
			}
			if !ValidIdentity(entry.ID, entry.Checksum) || seen[entry.ID] {
				return c, fmt.Errorf("%w: %d", ErrInvalidMapping, entry.ID)
			}
			seen[entry.ID] = true
			entry.Checksum, entry.Raw = strings.ToLower(entry.Checksum), raw
			c.Entries = append(c.Entries, entry)
		}
	}
	if len(c.Entries) == 0 {
		return c, fmt.Errorf("清单为空")
	}
	if _, err := ParseTime(c.Revision); err != nil {
		return c, err
	}
	return c, nil
}
