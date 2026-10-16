package videometa

import (
	"net/http"
	"testing"
)

func TestPlaybackLocationProtocol(t *testing.T) {
	path := "/files/1/42-v1.mp4?e=0123456789abcdef0123456789abcdef&s=36"
	request, _ := http.NewRequest("GET", "https://api.udon.dance/Api/Songs/play?id=42", nil)
	for _, tc := range []struct {
		name, location string
		status         int
		want           string
	}{
		{"relative", path, 302, "https://api.udon.dance" + path},
		{"network reference", "//play.udon.dance" + path, 307, "https://play.udon.dance" + path},
		{"http", "http://play.udon.dance" + path, 301, "http://play.udon.dance" + path},
		{"permanent", "https://play.udon.dance" + path, 308, "https://play.udon.dance" + path},
		{"missing", "", 302, ""},
		{"unsupported status", path, 303, ""},
		{"success is not redirect", path, 200, ""},
		{"credentials", "https://user:secret@play.udon.dance" + path, 302, ""},
		{"fragment", path + "#fragment", 302, ""},
		{"scheme", "ftp://play.udon.dance" + path, 302, ""},
		{"IP host", "https://127.0.0.1" + path, 302, ""},
		{"oversize", path + "0", 302, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Request: request, Header: http.Header{"Location": {tc.location}}}
			u, meta, err := PlaybackLocation(resp, 36)
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid response accepted", u)
				}
				return
			}
			if err != nil || u.String() != tc.want || meta.Size != 36 {
				t.Fatal(u, meta, err)
			}
		})
	}
}
