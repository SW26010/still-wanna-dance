package cacheproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogDigestV2Golden(t *testing.T) {
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := normalizeCatalog(c)
	const want = "sha256-v2:6f54580d842ce5dedcb4ab6fb67b90c29b6dd53a7a47036de97eb83b7f358cf4"
	if err != nil || got != want {
		t.Fatalf("digest contract changed: got %q, want %q (%v)", got, want, err)
	}
}

func TestCatalogDigestIgnoresOrderSourceAndUnknownFields(t *testing.T) {
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	second := c.Songs[0]
	second.ID = 1
	c.Songs = append(c.Songs, second)
	_, want, err := normalizeCatalog(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Source = "another-endpoint"
	c.Revision = "20261004000000"
	c.Songs[0], c.Songs[1] = c.Songs[1], c.Songs[0]
	c.Songs[0].MD5 = strings.ToUpper(c.Songs[0].MD5)
	_, got, err := normalizeCatalog(c)
	if err != nil || got != want {
		t.Fatal(got, want, err)
	}
	withUnknown := strings.Replace(catalogSample, `"id":90000`, `"futureDisplayField":42,"id":90000`, 1)
	extra, err := ParseCatalog([]byte(withUnknown), "test")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := ParseCatalog([]byte(catalogSample), "test")
	_, got, _ = normalizeCatalog(extra)
	_, want, _ = normalizeCatalog(base)
	if got != want {
		t.Fatal("unrecognized field changed digest")
	}
}

func TestCatalogDigestDetectsMetadataAndNullChanges(t *testing.T) {
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, _ := normalizeCatalog(c)
	for _, mutate := range []func(*CatalogSong){
		func(s *CatalogSong) { name := "different"; s.Name = &name },
		func(s *CatalogSong) { s.Flip = nil },
		func(s *CatalogSong) { s.Tags = json.RawMessage(`["new"]`) },
	} {
		changed := c
		changed.Songs = append([]CatalogSong(nil), c.Songs...)
		mutate(&changed.Songs[0])
		_, got, err := normalizeCatalog(changed)
		if err != nil || got == baseline {
			t.Fatal("content change not detected", got, err)
		}
	}
}
