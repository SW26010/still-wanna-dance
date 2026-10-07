package cacheproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Frozen digest v1 projection: never alias or embed CatalogSong here. Adding a
// field to an API/business type must not change persisted conflict decisions.
// Changes to this projection or normalization require an explicit new digest
// contract and a storage format decision.
type catalogDigestSongV1 struct {
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
	AyaID              json.RawMessage `json:"ayaId"` // Nullable number; preserve its exact JSON text.
	Tags               json.RawMessage `json:"tag"`
	OriginalURLs       json.RawMessage `json:"originalUrl"`
	ShaderMotion       json.RawMessage `json:"shaderMotion"`
}

// songs must be normalized and sorted by ID by normalizeCatalog. Source and
// revision are deliberately excluded: sources share the same content identity.
func catalogDigestV1(songs []CatalogSong) (string, error) {
	rows := make([]catalogDigestSongV1, 0, len(songs))
	for _, song := range songs {
		rows = append(rows, catalogDigestSongV1{
			ID:                 song.ID,
			MD5:                song.MD5,
			Name:               song.Name,
			Artist:             song.Artist,
			Dancer:             song.Dancer,
			PlayerCount:        song.PlayerCount,
			Volume:             song.Volume,
			Start:              song.Start,
			End:                song.End,
			Flip:               song.Flip,
			DoubleWidth:        song.DoubleWidth,
			SkipRandom:         song.SkipRandom,
			DisablePublic:      song.DisablePublic,
			RPE:                song.RPE,
			Genre:              song.Genre,
			Group:              song.Group,
			ComposedTitle:      song.ComposedTitle,
			ComposedTitleSpell: song.ComposedTitleSpell,
			AyaID:              song.AyaID,
			Tags:               song.Tags,
			OriginalURLs:       song.OriginalURLs,
			ShaderMotion:       song.ShaderMotion,
		})
	}
	body, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("still-wanna-dance/catalog/v1\n"), body...))
	return "sha256-v1:" + hex.EncodeToString(digest[:]), nil
}
