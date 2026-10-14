package cacheproxy

import (
	"net/http/httptest"
	"testing"
	"time"
)

func receiveDemandEvent[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for demand event")
		var zero T
		return zero
	}
}

func videoKey(body string) string {
	r := httptest.NewRequest("GET", videoURL(body), nil)
	return r.URL.Query().Get("e")
}
