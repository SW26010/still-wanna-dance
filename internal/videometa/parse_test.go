package videometa

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseProtocol(t *testing.T) {
	const path = "/files/123/42-aBc9.mp4"
	const checksum = "0123456789ABCDEF0123456789ABCDEF"
	const query = "e=" + checksum + "&s=1024"
	for _, tc := range []struct {
		name, path, query string
		limit             int64
		valid             bool
	}{
		{"valid", path, query, 1024, true},
		{"extra query", path, query + "&other=value", 1024, true},
		{"encoded path", strings.Replace(path, "42", "%34%32", 1), query, 1024, false},
		{"zero song", "/files/123/0-aBc9.mp4", query, 1024, false},
		{"leading zero song", "/files/123/042-aBc9.mp4", query, 1024, false},
		{"bad version", "/files/123/42-a_bc.mp4", query, 1024, false},
		{"bad extension", path + "x", query, 1024, false},
		{"duplicate checksum", path, query + "&e=" + checksum, 1024, false},
		{"duplicate size", path, query + "&s=1024", 1024, false},
		{"missing checksum", path, "s=1024", 1024, false},
		{"missing size", path, "e=" + checksum, 1024, false},
		{"bad query escape", path, query + "&x=%zz", 1024, false},
		{"short checksum", path, "e=abcd&s=1024", 1024, false},
		{"nonhex checksum", path, "e=" + strings.Repeat("z", 32) + "&s=1024", 1024, false},
		{"zero size", path, "e=" + checksum + "&s=0", 1024, false},
		{"negative size", path, "e=" + checksum + "&s=-1", 1024, false},
		{"overflow size", path, "e=" + checksum + "&s=9223372036854775808", 1024, false},
		{"over limit", path, query, 1023, false},
		{"zero limit", path, query, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.path + "?" + tc.query)
			if err != nil {
				t.Fatal(err)
			}
			before := *u
			got, err := Parse(u, tc.limit)
			if (err == nil) != tc.valid {
				t.Fatalf("metadata=%+v error=%v", got, err)
			}
			if *u != before {
				t.Fatal("mutated URL")
			}
			if tc.valid && got != (Metadata{ResourceID: "42", Version: "aBc9", Checksum: strings.ToLower(checksum), Size: 1024}) {
				t.Fatal(got)
			}
		})
	}
	if _, err := Parse(nil, 1024); err == nil {
		t.Fatal("accepted nil URL")
	}
}
