package cacheproxy

import (
	"strings"
	"testing"
)

const catalogSample = `{"code":200,"data":{"time":"20261003235746","groups":[{"entries":[{"id":90000,"name":"测试","artist":"测试","dancer":"测试","playerCount":1,"volume":1.0,"start":0,"end":8,"flip":true,"doubleWidth":false,"skipRandom":true,"disablePublic":true,"rpe":100,"genre":"","group":"Hide","composedTitle":"测试 - 测试 | 测试","composedTitleSpell":"c s - c s | c s","ayaId":null,"tag":[],"originalUrl":["https://www.bilibili.com/video/BV1Ds4y1Z7jp/?p=1"],"shaderMotion":[],"checksum":"0e49fdc94f704ed9520fa23a45b20f94"}]}]}}`

func TestCatalogRejectsInvalidCandidates(t *testing.T) {
	cases := []string{
		`{}`, `{"code":200,"data":{"time":"20261003235746","groups":[]}}`,
		strings.Replace(catalogSample, `"code":200`, `"code":500`, 1),
		strings.Replace(catalogSample, `"entries":[`, `"missing":[`, 1),
		strings.Replace(catalogSample, `"id":90000`, `"id":0`, 1),
		strings.Replace(catalogSample, `"tag":[]`, `"tag":{}`, 1),
		strings.Replace(catalogSample, `"ayaId":null`, `"ayaId":"12"`, 1),
		strings.Replace(catalogSample, `"flip":true`, `"flip":2`, 1),
		strings.Replace(catalogSample, `"volume":1.0`, `"volume":"1"`, 1),
		strings.Replace(catalogSample, `"20261003235746"`, `""`, 1),
		strings.Replace(catalogSample, `0e49fdc94f704ed9520fa23a45b20f94`, `invalid`, 1),
	}
	for _, body := range cases {
		if _, err := ParseCatalog([]byte(body), "test"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	c.Songs = append(c.Songs, c.Songs[0])
	if _, _, err := normalizeCatalog(c); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

func TestCatalogRejectsNonNumericAyaID(t *testing.T) {
	for _, value := range []string{`"external-42"`, `"12"`, `""`, `true`, `[]`, `{}`} {
		body := strings.Replace(catalogSample, `"ayaId":null`, `"ayaId":`+value, 1)
		if _, err := ParseCatalog([]byte(body), "test"); err == nil {
			t.Fatalf("accepted ayaId %s", value)
		}
	}
}

func TestCatalogRejectsUnsortableRevisions(t *testing.T) {
	for _, revision := range []string{"release-42", "9", "10", "2026100323574x", "202610032357460"} {
		if _, err := ParseCatalog([]byte(strings.Replace(catalogSample, "20261003235746", revision, 1)), "test"); err == nil {
			t.Fatalf("accepted unsupported revision %q", revision)
		}
	}
}
