package cacheproxy

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckLocalNeverPublishesOrDeletes(t *testing.T) {
	songs, cache := t.TempDir(), t.TempDir()
	target := videoURL(payload)
	parser := &Server{cfg: DefaultConfig()}
	v, err := parser.parse(httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, v.key+".mp4")
	for _, body := range []string{"broken", payload} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		hit, err := CheckLocal(context.Background(), songs, cache, target)
		if err != nil || hit != (body == payload) {
			t.Fatal(hit, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != body {
			t.Fatal("scan modified cache", err)
		}
		entries, err := os.ReadDir(songs)
		if err != nil || len(entries) != 0 {
			t.Fatal("scan published library", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckLocal(ctx, songs, cache, target); err != context.Canceled {
		t.Fatal(err)
	}
}
