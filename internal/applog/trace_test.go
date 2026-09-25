package applog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTraceAndTransportErrorPrivacy(t *testing.T) {
	ctx := WithTrace(context.Background())
	if TraceID(ctx) == "" || TraceID(WithTrace(ctx)) != TraceID(ctx) || TraceID(WithTrace(context.Background())) == TraceID(ctx) {
		t.Fatal("trace identity")
	}
	err := fmt.Errorf("upstream: %w", &url.Error{Op: "Get", URL: "http://example/video?token=secret", Err: context.DeadlineExceeded})
	safe := SafeError(err)
	if strings.Contains(safe.Error(), "secret") || !errors.Is(safe, context.DeadlineExceeded) {
		t.Fatal(safe)
	}
}

func TestMalformedRedirectErrorPrivacy(t *testing.T) {
	for _, location := range []string{
		"http://nya.xin.moe/%zz?token=secret",
		"/%zz?token=secret",
		"http://[invalid?token=secret",
	} {
		t.Run(location, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			_, err := client.Get(server.URL)
			if err == nil || !strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected real HTTP error containing Location: %v", err)
			}
			safe := SafeError(err)
			if strings.Contains(safe.Error(), "secret") || strings.Contains(safe.Error(), "token") {
				t.Fatalf("Location leaked: %v", safe)
			}
			if !strings.Contains(safe.Error(), "Location") {
				t.Fatalf("missing diagnostic category: %v", safe)
			}
			if !errors.Is(safe, err) {
				t.Fatal("lost error identity")
			}
			if SafeError(safe).Error() != safe.Error() {
				t.Fatal("repeated sanitization changed result")
			}
		})
	}
}
