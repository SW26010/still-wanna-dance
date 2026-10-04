package cacheproxy

import (
	"net/http/httptest"
	"testing"
)

func parsedVideo(t *testing.T, s *Server, body string) video {
	t.Helper()
	v, err := s.parse(httptest.NewRequest("GET", videoURL(body), nil))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
