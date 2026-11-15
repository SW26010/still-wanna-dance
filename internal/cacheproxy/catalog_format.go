package cacheproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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
	// Parse validates every identity before optional metadata so metadata errors
	// cannot mask invalid or duplicate ID/MD5 mappings in the catalog.
	c.Songs = make([]CatalogSong, 0, len(base.Entries))
	for _, entry := range base.Entries {
		var fields catalogSongFields
		if err := json.Unmarshal(entry.Raw, &fields); err != nil {
			return c, fmt.Errorf("清单资料格式无效: %w", err)
		}
		song, err := fields.song(base.Udon)
		if err != nil {
			return c, fmt.Errorf("清单资料格式无效: %w", err)
		}
		song.MD5, song.GroupName, song.Major = entry.Checksum, entry.GroupName, entry.Major
		c.Songs = append(c.Songs, song)
	}
	c, err = normalizeCatalog(c)
	return c, err
}

// Only the fields whose names or types differ between envelopes need deferred
// decoding. Common metadata is decoded directly, without a map/JSON round trip.
type catalogSongFields struct {
	CatalogSong
	DoubleWidth         json.RawMessage `json:"double_width"`
	SkipRandom          json.RawMessage `json:"skip_random"`
	DisablePublic       json.RawMessage `json:"disable_public"`
	ShaderMotion        json.RawMessage `json:"shader_motion"`
	BackupDoubleWidth   json.RawMessage `json:"doubleWidth"`
	BackupSkipRandom    json.RawMessage `json:"skipRandom"`
	BackupDisablePublic json.RawMessage `json:"disablePublic"`
	BackupShaderMotion  json.RawMessage `json:"shaderMotion"`
}

func (f catalogSongFields) song(udon bool) (CatalogSong, error) {
	song := f.CatalogSong
	doubleWidth, skipRandom, disablePublic := f.DoubleWidth, f.SkipRandom, f.DisablePublic
	song.ShaderMotion = f.ShaderMotion
	if !udon {
		doubleWidth, skipRandom, disablePublic = f.BackupDoubleWidth, f.BackupSkipRandom, f.BackupDisablePublic
		song.ShaderMotion = f.BackupShaderMotion
	} else if string(song.ShaderMotion) == `""` {
		song.ShaderMotion = json.RawMessage("[]")
	}
	for _, field := range []struct {
		raw     json.RawMessage
		target  **bool
		numeric bool
	}{
		{doubleWidth, &song.DoubleWidth, false},
		{skipRandom, &song.SkipRandom, udon},
		{disablePublic, &song.DisablePublic, udon},
	} {
		if len(field.raw) == 0 {
			continue
		}
		if field.numeric && (string(field.raw) == "0" || string(field.raw) == "1") {
			value := string(field.raw) == "1"
			*field.target = &value
		} else if err := json.Unmarshal(field.raw, field.target); err != nil {
			return song, err
		}
	}
	if string(bytes.TrimSpace(song.Tags)) == "null" {
		song.Tags = json.RawMessage("[]")
	}
	return song, nil
}

func normalizeCatalog(c Catalog) (Catalog, error) {
	if c.Source == "" || len(c.Songs) == 0 {
		return c, fmt.Errorf("清单为空或来源无效")
	}
	if _, err := ParseCatalogTime(c.Revision); err != nil {
		return c, err
	}
	c.Songs = append([]CatalogSong(nil), c.Songs...)
	sort.Slice(c.Songs, func(i, j int) bool { return c.Songs[i].ID < c.Songs[j].ID })
	for i := range c.Songs {
		s := &c.Songs[i]
		s.MD5 = strings.ToLower(s.MD5)
		if s.ID <= 0 || (s.MD5 != "" && !catalogmeta.ValidIdentity(s.ID, s.MD5)) || (i > 0 && c.Songs[i-1].ID == s.ID) {
			return c, fmt.Errorf("%w: %d", ErrInvalidCatalogMapping, s.ID)
		}
		// Udon serializes Unity single-precision volume with fewer decimal
		// digits than the backups. Compare/store their shared float32 value.
		if s.Volume != nil {
			v := float64(float32(*s.Volume))
			if math.IsInf(v, 0) || math.IsNaN(v) {
				return c, fmt.Errorf("清单音量无效: %d", s.ID)
			}
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
				return c, fmt.Errorf("清单数组格式无效: %w", err)
			}
			normalized, err := json.Marshal(value)
			if err != nil {
				return c, err
			}
			*raw = normalized
		}
	}
	return c, nil
}
