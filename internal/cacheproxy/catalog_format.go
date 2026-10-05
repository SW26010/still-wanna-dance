package cacheproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CatalogSong is the fixed upstream song schema. Pointers preserve unknown
// values independently from zero, false, and the empty string.
type CatalogSong struct {
	ID                 int64           `json:"id"`
	MD5                string          `json:"checksum"`
	Name               *string         `json:"name"`
	Artist             *string         `json:"artist"`
	Dancer             *string         `json:"dancer"`
	PlayerCount        *int64          `json:"playerCount"`
	Volume             *float64        `json:"volume"`
	Start              *float64        `json:"start"`
	End                *float64        `json:"end"`
	Flip               *bool           `json:"flip"`
	DoubleWidth        *bool           `json:"doubleWidth"`
	SkipRandom         *bool           `json:"skipRandom"`
	DisablePublic      *bool           `json:"disablePublic"`
	RPE                *int64          `json:"rpe"`
	Genre              *string         `json:"genre"`
	Group              *string         `json:"group"`
	ComposedTitle      *string         `json:"composedTitle"`
	ComposedTitleSpell *string         `json:"composedTitleSpell"`
	AyaID              *string         `json:"ayaId"`
	Tags               json.RawMessage `json:"tag"`
	OriginalURLs       json.RawMessage `json:"originalUrl"`
	ShaderMotion       json.RawMessage `json:"shaderMotion"`
}

type Catalog struct {
	Revision string
	Source   string
	Songs    []CatalogSong
}

// ParseCatalog accepts the shared MD5 catalog envelope. It rejects the whole
// candidate on malformed data rather than committing a partial reference set.
func ParseCatalog(body []byte, source string) (Catalog, error) {
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Time   string `json:"time"`
			Groups []struct {
				Entries []CatalogSong `json:"entries"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Catalog{}, fmt.Errorf("清单格式无效: %w", err)
	}
	c := Catalog{Revision: envelope.Data.Time, Source: source}
	if envelope.Code != 200 || envelope.Data.Groups == nil {
		return c, fmt.Errorf("清单格式无效")
	}
	for _, g := range envelope.Data.Groups {
		if g.Entries == nil {
			return c, fmt.Errorf("清单缺少 entries")
		}
		c.Songs = append(c.Songs, g.Entries...)
	}
	c, _, err := normalizeCatalog(c)
	return c, err
}

func normalizeCatalog(c Catalog) (Catalog, string, error) {
	if c.Source == "" || len(c.Songs) == 0 {
		return c, "", fmt.Errorf("清单为空或来源无效")
	}
	if _, err := time.Parse("20060102150405", c.Revision); err != nil {
		return c, "", fmt.Errorf("清单版本无效: %w", err)
	}
	c.Songs = append([]CatalogSong(nil), c.Songs...)
	sort.Slice(c.Songs, func(i, j int) bool { return c.Songs[i].ID < c.Songs[j].ID })
	for i := range c.Songs {
		s := &c.Songs[i]
		s.MD5 = strings.ToLower(s.MD5)
		if s.ID <= 0 || !validMD5(s.MD5) || (i > 0 && c.Songs[i-1].ID == s.ID) {
			return c, "", fmt.Errorf("清单 ID 或 MD5 无效或重复: %d", s.ID)
		}
		for _, raw := range []*json.RawMessage{&s.Tags, &s.OriginalURLs, &s.ShaderMotion} {
			if len(*raw) == 0 || string(bytes.TrimSpace(*raw)) == "null" {
				*raw = nil
				continue
			}
			var value []any
			decoder := json.NewDecoder(bytes.NewReader(*raw))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return c, "", fmt.Errorf("清单数组格式无效: %w", err)
			}
			normalized, err := json.Marshal(value)
			if err != nil {
				return c, "", err
			}
			*raw = normalized
		}
	}
	body, err := json.Marshal(c.Songs)
	if err != nil {
		return c, "", err
	}
	digest := sha256.Sum256(body)
	return c, hex.EncodeToString(digest[:]), nil
}
