package cacheproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogDigestV1Golden(t *testing.T) {
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := normalizeCatalog(c)
	const want = "sha256-v1:e841c13f7d3329eddd414d34618d255a1f512557e9e7161a6b7b6faab71081bb"
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

func TestCatalogDigestDetectsMappingMetadataAndNullChanges(t *testing.T) {
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, _ := normalizeCatalog(c)
	for _, mutate := range []func(*CatalogSong){
		func(s *CatalogSong) { s.MD5 = strings.Repeat("a", 32) },
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
