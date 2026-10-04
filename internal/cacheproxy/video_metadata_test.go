package cacheproxy

import (
	"net/http"
	"testing"
)

func TestSharedMetadataPreservesCacheIdentity(t *testing.T) {
	const query = "?e=0123456789ABCDEF0123456789ABCDEF&s=1024"
	expected := "0123456789abcdef0123456789abcdef"
	for _, target := range []string{
		"https://play.udon.dance/files/123/42-aBc9.mp4" + query,
		"http://NYA.XIN.MOE:8080/files/456/99-different.mp4" + query,
	} {
		r, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		v, err := parseVideo(r, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if v.key != expected || v.size != 1024 || v.checksum != "0123456789abcdef0123456789abcdef" {
			t.Fatal(v)
		}
	}
	if err := ValidateVideoURL("https://play.udon.dance/files/123/%34%32-aBc9.mp4"+query, 1024); err == nil {
		t.Fatal("accepted encoded path")
	}
}
