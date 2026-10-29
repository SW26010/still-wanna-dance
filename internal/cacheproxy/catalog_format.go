package cacheproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"still-wanna-dance/internal/catalogmeta"
)

var ErrInvalidCatalogMapping = catalogmeta.ErrInvalidMapping

// CatalogSong is the fixed upstream song schema. Pointers preserve unknown
// values independently from zero, false, and the empty string.
type CatalogSong struct {
	ID            int64           `json:"id"`
	MD5           string          `json:"checksum"`
	Name          *string         `json:"name"`
	Artist        *string         `json:"artist"`
	Dancer        *string         `json:"dancer"`
	PlayerCount   *int64          `json:"playerCount"`
	Volume        *float64        `json:"volume"`
	Start         *float64        `json:"start"`
	End           *float64        `json:"end"`
	Flip          *bool           `json:"flip"`
	DoubleWidth   *bool           `json:"double_width"`
	SkipRandom    *bool           `json:"skip_random"`
	DisablePublic *bool           `json:"disable_public"`
	RPE           *int64          `json:"rpe"`
	Major         *string         `json:"major"`
	GroupName     *string         `json:"groupName"`
	Tags          json.RawMessage `json:"tag"`
	ShaderMotion  json.RawMessage `json:"shader_motion"`
}

type Catalog struct {
	Revision   string
	Source     string
	Songs      []CatalogSong
	ObservedAt time.Time
}

// ParseCatalogTime reads the civil timestamp supplied by all three catalog
// endpoints. They publish YYYYMMDDhhmmss without an offset, at second precision.
// UTC here is a common comparison axis, not a claim about the publisher's zone.
func ParseCatalogTime(value string) (time.Time, error) {
	return catalogmeta.ParseTime(value)
}

// ParseCatalog adapts Udon and both backup envelopes to the Udon song schema.
// Malformed candidates are rejected as a whole, never partially committed.
func ParseCatalog(body []byte, source string) (Catalog, error) {
	base, err := catalogmeta.Parse(body)
	c := Catalog{Revision: base.Revision, Source: source, ObservedAt: time.Now()}
	if err != nil {
		return c, err
	}
	// Validate every identity before optional metadata so metadata errors cannot
	// mask invalid or duplicate ID/MD5 mappings in the catalog.
	for _, entry := range base.Entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry.Raw, &fields); err != nil {
			return c, err
		}
		if !base.Udon {
			for from, to := range map[string]string{"doubleWidth": "double_width", "skipRandom": "skip_random", "disablePublic": "disable_public", "shaderMotion": "shader_motion"} {
				fields[to] = fields[from]
				if fields[to] == nil {
					delete(fields, to)
				}
			}
		} else {
			for _, key := range []string{"skip_random", "disable_public"} {
				switch string(fields[key]) {
				case "0":
					fields[key] = json.RawMessage("false")
				case "1":
					fields[key] = json.RawMessage("true")
				}
			}
			if string(fields["shader_motion"]) == `""` {
				fields["shader_motion"] = json.RawMessage("[]")
			}
		}
		if string(bytes.TrimSpace(fields["tag"])) == "null" {
			fields["tag"] = json.RawMessage("[]")
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return c, err
		}
		var song CatalogSong
		if err := json.Unmarshal(raw, &song); err != nil {
			return c, fmt.Errorf("清单资料格式无效: %w", err)
		}
		song.MD5, song.GroupName, song.Major = entry.Checksum, entry.GroupName, entry.Major
		c.Songs = append(c.Songs, song)
	}
	c, _, err = normalizeCatalog(c)
	return c, err
}

func normalizeCatalog(c Catalog) (Catalog, string, error) {
	if c.Source == "" || len(c.Songs) == 0 {
		return c, "", fmt.Errorf("清单为空或来源无效")
	}
	if _, err := ParseCatalogTime(c.Revision); err != nil {
		return c, "", err
	}
	c.Songs = append([]CatalogSong(nil), c.Songs...)
	sort.Slice(c.Songs, func(i, j int) bool { return c.Songs[i].ID < c.Songs[j].ID })
	for i := range c.Songs {
		s := &c.Songs[i]
		s.MD5 = strings.ToLower(s.MD5)
		if s.ID <= 0 || (s.MD5 != "" && !catalogmeta.ValidIdentity(s.ID, s.MD5)) || (i > 0 && c.Songs[i-1].ID == s.ID) {
			return c, "", fmt.Errorf("%w: %d", ErrInvalidCatalogMapping, s.ID)
		}
		// Udon serializes Unity single-precision volume with fewer decimal
		// digits than the backups. Compare/store their shared float32 value.
		if s.Volume != nil {
			v := float64(float32(*s.Volume))
			s.Volume = &v
		}
		for _, raw := range []*json.RawMessage{&s.Tags, &s.ShaderMotion} {
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
	digest, err := catalogDigestV2(c.Songs)
	return c, digest, err
}
