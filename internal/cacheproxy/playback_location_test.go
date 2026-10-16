package cacheproxy

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestStandalonePlaybackResolvesRelativeLocation(t *testing.T) {
	s, _ := setup(t, nil)
	u, _ := url.Parse(videoURL(payload))
	calls := 0
	s.client.Transport = resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {u.RequestURI()}}, Body: http.NoBody, Request: r}, nil
	})
	v, err := s.resolvePlaybackVideo(context.Background(), url.Values{"id": {"42"}, "node": {"cf"}})
	if err != nil || v.host != "api.udon.dance" || v.size != int64(len(payload)) || calls != 1 {
		t.Fatal(v, calls, err)
	}
}
