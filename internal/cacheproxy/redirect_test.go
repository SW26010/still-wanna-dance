package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type redirectTransport func(*http.Request) (*http.Response, error)

func (f redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCurrentVideoRedirectStatuses(t *testing.T) {
	for _, status := range []int{200, 300, 301, 302, 303, 304, 305, 306, 307, 308, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := &Server{cfg: DefaultConfig(), client: &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				Transport: redirectTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://api.udon.dance/Api/Songs/play?id=1344&node=nya" {
						t.Fatalf("unexpected request: %s", r.URL)
					}
					w := httptest.NewRecorder()
					w.Header().Set("Location", videoURL(payload))
					w.WriteHeader(status)
					return w.Result(), nil
				}),
			}}
			got, err := s.resolvePlaybackVideoReadOnly(context.Background(), url.Values{"id": {"1344"}, "node": {"nya"}})
			accepted := status == 301 || status == 302 || status == 307 || status == 308
			if accepted {
				want, parseErr := s.parse(httptest.NewRequest(http.MethodGet, videoURL(payload), nil))
				if parseErr != nil || err != nil || got.key != want.key {
					t.Fatalf("got %q, %v; want %q, %v", got.key, err, want.key, parseErr)
				}
			} else if err == nil {
				t.Fatalf("accepted status %d", status)
			}
		})
	}
}
