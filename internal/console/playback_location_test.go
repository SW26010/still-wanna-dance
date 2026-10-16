package console

import (
	"context"
	"net/http"
	"testing"
)

func TestBusinessPlaybackResolvesRelativeLocation(t *testing.T) {
	c := testConsole(t)
	c.apiBase = "https://api.udon.dance"
	path := "/files/1/42-v1.mp4?e=0123456789abcdef0123456789abcdef&s=36"
	calls := 0
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {path}}, Body: http.NoBody, Request: r}, nil
	})}
	got, err := c.resolveNodeWithClient(context.Background(), 42, "cf", client)
	if err != nil || got != c.apiBase+path || calls != 1 {
		t.Fatal(got, calls, err)
	}
}
