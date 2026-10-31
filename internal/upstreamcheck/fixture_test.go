package upstreamcheck

import (
	"io"
	"net/http"
	"strings"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f(r)
	if resp != nil {
		resp.Request = r
	}
	return resp, err
}

const catalogFixture = `{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`
const kivaFixture = `{"code":200,"data":{"time":"20261005000000","groups":[{"entries":[{"id":42,"checksum":"0123456789abcdef0123456789abcdef"}]}]}}`
const videoFixture = "https://play.udon.dance/files/123/42-abc.mp4?e=0123456789abcdef0123456789abcdef&s=131072"

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func fixture(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Range") == "bytes=0-0" {
		resp := response(206, "x")
		resp.Header.Set("Content-Range", "bytes 0-0/131072")
		return resp, nil
	}
	if r.URL.Host == "x.kiva.moe" || r.URL.Host == "wanna.kiva.moe" {
		return response(200, kivaFixture), nil
	}
	if r.URL.Path == "/Api/Songs/list" {
		return response(200, catalogFixture), nil
	}
	if r.URL.Path == "/Api/Songs/play" {
		u := videoFixture
		if r.URL.Query().Get("node") == "nya" {
			u = strings.Replace(u, "play.udon.dance", "nya.xin.moe", 1)
		}
		resp := response(302, "")
		resp.Header.Set("Location", u)
		return resp, nil
	}
	resp := response(206, strings.Repeat("x", 131072))
	resp.Header.Set("Content-Range", "bytes 0-131071/131072")
	return resp, nil
}
