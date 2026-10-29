package cacheproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Frozen v2 projection contains only shared song metadata. MD5 is resource
// evidence and never participates in metadata version conflicts.
type catalogDigestSongV2 struct {
	ID            int64           `json:"id"`
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

func catalogDigestV2(songs []CatalogSong) (string, error) {
	rows := make([]catalogDigestSongV2, 0, len(songs))
	for _, s := range songs {
		rows = append(rows, catalogDigestSongV2{s.ID, s.Name, s.Artist, s.Dancer, s.PlayerCount, s.Volume, s.Start, s.End, s.Flip, s.DoubleWidth, s.SkipRandom, s.DisablePublic, s.RPE, s.Major, s.GroupName, s.Tags, s.ShaderMotion})
	}
	body, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("still-wanna-dance/catalog/v2\n"), body...))
	return "sha256-v2:" + hex.EncodeToString(digest[:]), nil
}
