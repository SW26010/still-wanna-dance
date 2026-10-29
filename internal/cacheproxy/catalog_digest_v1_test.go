package cacheproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
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
func catalogDigestV1(songs []catalogDigestSongV1) (string, error) {
	rows := songs
	body, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("still-wanna-dance/catalog/v1\n"), body...))
	return "sha256-v1:" + hex.EncodeToString(digest[:]), nil
}

// v1 remains frozen historical evidence; it is not accepted by the v8 store.
func TestHistoricalCatalogDigestV1(t *testing.T) {
	var envelope struct {
		Data struct {
			Groups []struct{ Entries []catalogDigestSongV1 }
		}
	}
	if err := json.Unmarshal([]byte(catalogSample), &envelope); err != nil {
		t.Fatal(err)
	}
	got, err := catalogDigestV1(envelope.Data.Groups[0].Entries)
	const want = "sha256-v1:e841c13f7d3329eddd414d34618d255a1f512557e9e7161a6b7b6faab71081bb"
	if err != nil || got != want {
		t.Fatal(got, err)
	}
}
