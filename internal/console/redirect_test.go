package console

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveNodeRedirectStatuses(t *testing.T) {
	for _, upstream := range []string{"hkg", "cf"} {
		for _, status := range []int{200, 300, 301, 302, 303, 304, 305, 306, 307, 308, 404, 500} {
			t.Run(fmt.Sprintf("%s/%d", upstream, status), func(t *testing.T) {
				host, node := "nya.xin.moe", "nya"
				if upstream == "cf" {
					host, node = "play.udon.dance", "cf"
				}
				target := "https://" + host + "/files/2403/1344-test.mp4?e=0123456789abcdef0123456789abcdef&s=10"
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/Api/Songs/play" || r.URL.Query().Get("node") != node || r.URL.Query().Get("id") != "1344" {
						t.Errorf("unexpected request: %s", r.URL)
					}
					w.Header().Set("Location", target)
					w.WriteHeader(status)
				}))
				defer api.Close()
				c := &Console{apiBase: api.URL, client: &http.Client{
					CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				}}
				got, err := c.resolveNode(context.Background(), 1344, upstream)
				accepted := status == 301 || status == 302 || status == 307 || status == 308
				if accepted && (err != nil || got != target) {
					t.Fatalf("got %q, %v; want %q", got, err, target)
				}
				if !accepted && err == nil {
					t.Fatalf("accepted status %d", status)
				}
			})
		}
	}
}
